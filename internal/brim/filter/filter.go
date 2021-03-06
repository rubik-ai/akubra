package filter

import (
	"fmt"
	"strings"

	"github.com/AdRoll/goamz/s3"
	"github.com/allegro/akubra/internal/akubra/log"
	"github.com/allegro/akubra/internal/akubra/sharding"
	"github.com/allegro/akubra/internal/akubra/storages"
	"github.com/allegro/akubra/internal/akubra/watchdog"
	"github.com/allegro/akubra/internal/brim/auth"
	"github.com/allegro/akubra/internal/brim/model"
	brimS3 "github.com/allegro/akubra/internal/brim/s3"
)

//WALFilter consults the storages to determine the desired state of an object
type WALFilter interface {
	Filter(walEntriesChannel <-chan *model.WALEntry) <-chan *model.WALTask
}
type domain string

//DefaultWALFilter is a default implementation of WALFilter
type DefaultWALFilter struct {
	WALFilter
	backendResolver auth.BackendResolver
	rings           map[domain]sharding.ShardsRingAPI
	versionFetcher  VersionFetcher
}

type storageEndpoint = string
type keys struct {
	access string
	secret string
}

type objectState struct {
	storagesKeys          map[storageEndpoint]keys
	storagesWithObject    []*StorageState
	storagesWithoutObject []*StorageState
}

type storagesEndpoints struct {
	src                                  string
	destinations                         []string
	numberOfStoragesWithoutVersionHeader int
}

type ringState struct {
	oldStoragesWithObject []*s3.S3
	targetShardSrcCli     *s3.S3
	targetShardDstClis    []*s3.S3
}

var noopTask = ringState{nil, nil, nil}

//NewDefaultWALFilter constructs an instance of DefaultWALFeeder
func NewDefaultWALFilter(resolver auth.BackendResolver, fetcher VersionFetcher) WALFilter {
	return &DefaultWALFilter{
		backendResolver: resolver,
		rings:           make(map[domain]sharding.ShardsRingAPI),
		versionFetcher:  fetcher,
	}
}

//Filter filters that rows acquired from the database and creates WALTasks for them
func (filter *DefaultWALFilter) Filter(walEntriesChannel <-chan *model.WALEntry) <-chan *model.WALTask {
	tasksChannel := make(chan *model.WALTask, len(walEntriesChannel))
	go func() {
		for walEntry := range walEntriesChannel {

			log.Debugf("Processing WALEntry for reqID = '%s' objID = '%s'",
				walEntry.Record.RequestID, walEntry.Record.ObjectID)

			ring, err := filter.determineRing(walEntry)
			if err != nil {
				finishWithError(walEntry, err)
				continue
			}

			ringState, err := filter.determineStorages(walEntry.Record, ring)
			if err != nil {
				finishWithError(walEntry, err)
				continue
			}

			//we're removing the hook from this task, because we need to run it only only after we've also deleted
			// the object's versions from storages that used to be the object's storages
			// (for example before a migration, or before weights were changed)
			hook := noopHook
			if len(ringState.oldStoragesWithObject) > 0 {
				hook = walEntry.RecordProcessedHook
				walEntry.RecordProcessedHook = noopHook
			}

			tasksChannel <- &model.WALTask{
				WALEntry:            walEntry,
				SourceClient:        ringState.targetShardSrcCli,
				DestinationsClients: ringState.targetShardDstClis,
			}

			tasksChannel <- clearOldStoragesTask(walEntry.Record, hook, ringState.oldStoragesWithObject)
		}
	}()
	return tasksChannel
}

func clearOldStoragesTask(record *watchdog.ConsistencyRecord, recordProcessedHook model.Hook, s3Clis []*s3.S3) *model.WALTask {
	deleteRecord := *record
	deleteRecord.Method = watchdog.DELETE
	return &model.WALTask{
		DestinationsClients: s3Clis,
		SourceClient:        nil,
		WALEntry: &model.WALEntry{
			Record:              &deleteRecord,
			RecordProcessedHook: recordProcessedHook,
		},
	}
}

func finishWithError(entry *model.WALEntry, err error) {
	hookErr := entry.RecordProcessedHook(entry.Record, err)
	if hookErr != nil {
		log.Debug(hookErr)
	}
}

func (filter *DefaultWALFilter) determineStorages(record *watchdog.ConsistencyRecord, ring sharding.ShardsRingAPI) (*ringState, error) {
	pickedShard, err := ring.Pick(record.ObjectID)
	if err != nil {
		return nil, err
	}

	var pickedShardSrcCli *s3.S3
	var pickedShardDstClis []*s3.S3
	var oldStoragesWithObject []*s3.S3

	for _, shardClient := range ring.GetShards() {

		stateOnShard, err := filter.fetchVersionsFromStorages(record, shardClient)
		if err != nil {
			return nil, err
		}

		if shardClient == pickedShard {
			srcCli, dstClis, err := filter.prepareShardMigration(record, stateOnShard)
			if srcCli == nil && dstClis == nil && err == nil {
				return &noopTask, nil
			}
			pickedShardSrcCli = srcCli
			pickedShardDstClis = dstClis
			continue
		}

		oldStoragesWithObject = append(
			oldStoragesWithObject,
			filter.getStoragesWithVersion(record.ObjectVersion, stateOnShard)...)
	}

	return &ringState{
		oldStoragesWithObject: oldStoragesWithObject,
		targetShardSrcCli:     pickedShardSrcCli,
		targetShardDstClis:    pickedShardDstClis,
	}, nil
}

func (filter *DefaultWALFilter) fetchVersionsFromStorages(record *watchdog.ConsistencyRecord, shardClient storages.NamedShardClient) (*objectState, error) {
	storagesKeys, err := filter.resolveStoragesKeys(record, shardClient)
	if err != nil {
		return nil, err
	}
	storagesWithObject, storagesWithoutObject, err := filter.checkStoragesForObjectPresence(storagesKeys, record, shardClient)
	if err != nil {
		return nil, err
	}
	return &objectState{
		storagesKeys:          storagesKeys,
		storagesWithObject:    storagesWithObject,
		storagesWithoutObject: storagesWithoutObject,
	}, nil
}

func resolveVersions(record *watchdog.ConsistencyRecord, objectState *objectState) (*storagesEndpoints, error) {
	if record.Method == watchdog.PUT && len(objectState.storagesWithObject) < 1 {
		log.Printf("object '%s' in domain '%s' is not present on any storage", record.ObjectID, record.Domain)
		return nil, nil
	}

	storagesEndpoints, err := checkVersions(record, objectState)
	if err != nil || storagesEndpoints == nil {
		return nil, err
	}

	if storagesEndpoints.numberOfStoragesWithoutVersionHeader == len(objectState.storagesWithObject) {
		log.Printf("No object '%s/%s' with version found on the storages, keeping the version from storages",
			record.Domain, record.ObjectID)
		return nil, nil
	}
	if record.Method == watchdog.PUT {
		for _, storage := range objectState.storagesWithoutObject {
			storagesEndpoints.destinations = append(storagesEndpoints.destinations, storage.storageEndpoint)
		}
	}
	return storagesEndpoints, nil
}

func checkVersions(record *watchdog.ConsistencyRecord, objectState *objectState) (*storagesEndpoints, error) {

	var srcStorageEndpoint string
	var storagesEndpointsToSync []string
	var numberOfStoragesWithoutVersionHeader = 0

	for _, storage := range objectState.storagesWithObject {

		if storage.version == -1 {
			if !storage.objectNotFound {
				numberOfStoragesWithoutVersionHeader++
			}
			storagesEndpointsToSync = append(storagesEndpointsToSync, storage.storageEndpoint)
			continue
		}

		//There is a newer version of the object on one of the storage, so there also must be record
		//for that object written to the log, so we can just skip this record an remove it
		if storage.version > record.ObjectVersion {
			return nil, nil
		}

		switch record.Method {
		case watchdog.PUT:
			if storage.version < record.ObjectVersion {
				storagesEndpointsToSync = append(storagesEndpointsToSync, storage.storageEndpoint)
			} else {
				srcStorageEndpoint = storage.storageEndpoint
			}
		case watchdog.DELETE:
			if !storage.objectNotFound {
				storagesEndpointsToSync = append(storagesEndpointsToSync, storage.storageEndpoint)
			}
		}
	}
	return &storagesEndpoints{
		src:                                  srcStorageEndpoint,
		destinations:                         storagesEndpointsToSync,
		numberOfStoragesWithoutVersionHeader: numberOfStoragesWithoutVersionHeader,
	}, nil
}

func (filter *DefaultWALFilter) createS3Clients(endpoints []string, storagesKeys map[storageEndpoint]keys) []*s3.S3 {
	clients := make([]*s3.S3, len(endpoints))
	for idx := range endpoints {
		clientAuth := &brimS3.MigrationAuth{
			AccessKey: storagesKeys[endpoints[idx]].access,
			SecretKey: storagesKeys[endpoints[idx]].secret,
			Endpoint:  endpoints[idx],
		}
		clientAuth.Endpoint = endpoints[idx]
		clients[idx] = brimS3.GetS3Client(clientAuth)
	}
	return clients
}

func (filter *DefaultWALFilter) determineRing(entry *model.WALEntry) (sharding.ShardsRingAPI, error) {
	objDomain := domain(entry.Record.Domain)
	ring, ringInCache := filter.rings[objDomain]
	if ringInCache {
		return ring, nil
	}
	resolvedRing, err := filter.backendResolver.GetShardsRing(entry.Record.Domain)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve ring: %s", err.Error())
	}
	filter.rings[objDomain] = resolvedRing
	return resolvedRing, nil
}

func (filter *DefaultWALFilter) resolveStoragesKeys(record *watchdog.ConsistencyRecord, shardClient storages.NamedShardClient) (map[storageEndpoint]keys, error) {
	storagesKeys := make(map[storageEndpoint]keys)
	for _, storageClient := range shardClient.Backends() {
		s3Client, err := filter.
			backendResolver.
			ResolveClientForBackend(storageClient.Name, record.ObjectID, record.AccessKey)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve secret key for %s: %s", storageClient.Name, err)
		}
		storagesKeys[storageClient.Endpoint.String()] = keys{
			access: s3Client.AccessKey,
			secret: s3Client.SecretKey,
		}
	}
	return storagesKeys, nil
}

func (filter *DefaultWALFilter) checkStoragesForObjectPresence(storagesKeys map[storageEndpoint]keys, record *watchdog.ConsistencyRecord, shardClient storages.NamedShardClient) ([]*StorageState, []*StorageState, error) {
	bucketAndKey := strings.Split(record.ObjectID, "/")
	if len(bucketAndKey) < 2 || bucketAndKey[0] == "" || bucketAndKey[1] == "" {
		return nil, nil, fmt.Errorf("malformed object's path '%s", record.ObjectID)
	}

	var storagesWithObject, storagesWithoutObject []*StorageState

	for _, storageClient := range shardClient.Backends() {

		clientAuth := &brimS3.MigrationAuth{
			AccessKey: storagesKeys[storageClient.Endpoint.String()].access,
			SecretKey: storagesKeys[storageClient.Endpoint.String()].secret,
			Endpoint:  storageClient.Endpoint.String(),
		}

		objState, err := filter.versionFetcher.Fetch(clientAuth, bucketAndKey[0], bucketAndKey[1])
		if err != nil {
			return nil, nil, fmt.Errorf("couldn't determine object '%s' version on storage '%s': %s",
				record.ObjectID, storageClient.Endpoint.String(), err)
		}

		if objState.objectNotFound {
			log.Printf("Object '%s' is not present on storage '%s'", record.ObjectID, clientAuth.Endpoint)
			storagesWithoutObject = append(storagesWithoutObject, objState)
			continue
		}

		log.Debugf("Object's '%s' version on storage '%s' is '%s'",
			record.ObjectID, objState.storageEndpoint, objState.version)
		storagesWithObject = append(storagesWithObject, objState)
	}

	return storagesWithObject, storagesWithoutObject, nil
}

func (filter *DefaultWALFilter) getStoragesWithVersion(version int, state *objectState) []*s3.S3 {
	var storagesWithObject []*s3.S3
	for _, storageWithObject := range state.storagesWithObject {
		if storageWithObject.objectNotFound {
			continue
		}
		if storageWithObject.version <= version {
			storageClient := filter.createS3Clients([]string{storageWithObject.storageEndpoint}, state.storagesKeys)[0]
			storagesWithObject = append(storagesWithObject, storageClient)
		}
	}
	return storagesWithObject
}

func (filter *DefaultWALFilter) prepareShardMigration(record *watchdog.ConsistencyRecord, state *objectState) (*s3.S3, []*s3.S3, error) {
	storagesEndpoints, err := resolveVersions(record, state)
	if err != nil {
		return nil, nil, err
	}
	//In this the case there is a newer version of the object on at least
	//one of the storages
	if storagesEndpoints == nil && err == nil {
		return nil, nil, nil
	}
	var srcStorages []string
	if storagesEndpoints.src != "" {
		srcStorages = []string{storagesEndpoints.src}
	}

	srcClients := filter.createS3Clients(srcStorages, state.storagesKeys)
	dstClients := filter.createS3Clients(storagesEndpoints.destinations, state.storagesKeys)

	var srcClient *s3.S3
	if record.Method == watchdog.PUT {
		if len(srcClients) == 0 {
			return nil, nil, nil
		}
		srcClient = srcClients[0]
	}

	return srcClient, dstClients, err
}
func noopHook(_ *watchdog.ConsistencyRecord, _ error) error {
	return nil
}
