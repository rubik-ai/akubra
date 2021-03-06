package sharding

import (
	"fmt"
	"github.com/allegro/akubra/internal/akubra/config"
	"github.com/allegro/akubra/internal/akubra/watchdog"
	"math"

	"github.com/allegro/akubra/internal/akubra/log"
	regionsConfig "github.com/allegro/akubra/internal/akubra/regions/config"
	"github.com/allegro/akubra/internal/akubra/storages"
	"github.com/serialx/hashring"
)

// RingFactory produces clients ShardsRing
type RingFactory struct {
	conf                  config.Config
	storages              storages.ClusterStorage
	syncLog               log.Logger
	consistencyWatchdog   watchdog.ConsistencyWatchdog
	recordFactory         watchdog.ConsistencyRecordFactory
	consistencyHeaderName string
}

func (rf RingFactory) createRegressionMap(config regionsConfig.Policies) (map[string]storages.NamedShardClient, error) {
	regressionMap := make(map[string]storages.NamedShardClient)
	lastClusterName := config.Shards[len(config.Shards)-1].ShardName
	previousCluster, err := rf.storages.GetShard(lastClusterName)
	if err != nil {
		log.Printf("Last cluster in region not defined in storages")
	}
	for _, cluster := range config.Shards {
		clientCluster, err := rf.storages.GetShard(cluster.ShardName)
		if err != nil {
			return nil, err
		}
		regressionMap[cluster.ShardName] = previousCluster
		previousCluster = clientCluster
	}
	return regressionMap, nil
}

func (rf RingFactory) getRegionClustersWeights(regionCfg regionsConfig.Policies) map[string]int {
	res := make(map[string]int)
	for _, clusterConfig := range regionCfg.Shards {
		res[clusterConfig.ShardName] = int(math.Floor(clusterConfig.Weight * 100))
	}
	return res
}

func (rf RingFactory) makeRegionClusterMap(clientClusters map[string]int) (map[string]storages.NamedShardClient, error) {
	res := make(map[string]storages.NamedShardClient, len(clientClusters))
	for name := range clientClusters {
		cl, err := rf.storages.GetShard(name)
		if err != nil {
			return nil, err
		}
		res[name] = cl
	}
	return res, nil
}

// RegionRing returns ShardsRing for region
func (rf RingFactory) RegionRing(name string, conf config.Config, regionCfg regionsConfig.Policies) (ShardsRingAPI, error) {
	clustersWeights := rf.getRegionClustersWeights(regionCfg)

	shardClusterMap, err := rf.makeRegionClusterMap(clustersWeights)
	for name, shard := range shardClusterMap {
		s := shard
		if rf.consistencyWatchdog != nil {
			s = storages.NewConsistentShard(s, rf.consistencyWatchdog, rf.recordFactory, rf.consistencyHeaderName)
		}
		s = storages.NewShardAuthenticator(s, rf.conf.IgnoredCanonicalizedHeaders)
		shardClusterMap[name] = s
	}
	if err != nil {
		log.Debugf("cluster map creation error %s\n", err)
		return ShardsRing{}, err
	}
	var regionShards []storages.NamedShardClient
	for _, cluster := range shardClusterMap {
		regionShards = append(regionShards, cluster)
	}

	cHashMap := hashring.NewWithWeights(clustersWeights)

	allBackendsRoundTripper := rf.storages.MergeShards(fmt.Sprintf("region-%s", name), regionShards...)
	if rf.consistencyWatchdog != nil {
		allBackendsRoundTripper = storages.NewConsistentShard(
			allBackendsRoundTripper, rf.consistencyWatchdog,
			rf.recordFactory, rf.consistencyHeaderName)
	}
	allBackendsRoundTripper = storages.NewShardAuthenticator(allBackendsRoundTripper, nil)
	regressionMap, err := rf.createRegressionMap(regionCfg)
	if err != nil {
		return ShardsRing{}, err
	}

	return ShardsRing{
		ring:                      cHashMap,
		shardClusterMap:           shardClusterMap,
		allClustersRoundTripper:   allBackendsRoundTripper,
		watchdogVersionHeaderName: conf.Watchdog.ObjectVersionHeaderName,
		clusterRegressionMap:      regressionMap,
		ringProps: &RingProps{
			ConsistencyLevel: regionCfg.ConsistencyLevel,
			ReadRepair:       regionCfg.ReadRepair,
		}}, nil
}

// NewRingFactory creates ring factory
func NewRingFactory(conf config.Config, storages storages.ClusterStorage,
	consistencyWatchdog watchdog.ConsistencyWatchdog,
	recordFactory watchdog.ConsistencyRecordFactory,
	consistencyHeaderName string) RingFactory {
	return RingFactory{
		conf:                  conf,
		storages:              storages,
		consistencyWatchdog:   consistencyWatchdog,
		recordFactory:         recordFactory,
		consistencyHeaderName: consistencyHeaderName,
	}
}
