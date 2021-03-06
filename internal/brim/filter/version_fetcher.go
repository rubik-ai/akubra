package filter

import (
	"fmt"
	"strconv"

	"github.com/allegro/akubra/internal/brim/s3"
)

//VersionFetcher fetches object's version
type VersionFetcher interface {
	//Fetch should fetch object's version
	Fetch(auth *s3.MigrationAuth, bucketName string, key string) (*StorageState, error)
}

//S3VersionFetcher is an implementation of VersionFetcher that uses an S3 client
type S3VersionFetcher struct {
	VersionHeaderName string
}

//StorageState describes if an object is present on o storage and if so, in which version
type StorageState struct {
	storageEndpoint string
	version         int
	objectNotFound  bool
}

//Fetch fetches the object's version using s3 client
func (s3VersionFetcher *S3VersionFetcher) Fetch(auth *s3.MigrationAuth, bucketName string, key string) (*StorageState, error) {
	s3Client := s3.GetS3Client(auth)
	bucket := s3Client.Bucket(bucketName)
	headResponse, err := bucket.Head(key, nil)
	if err != nil {
		if err.Error() == "404 Not Found" {
			return &StorageState{
				objectNotFound:  true,
				version:         -1,
				storageEndpoint: s3Client.S3Endpoint,
			}, nil
		}
		return nil, err
	}
	if headResponse.StatusCode != 200 {
		return nil, fmt.Errorf("bad response, status code = %d, message = %s",
			headResponse.StatusCode, headResponse.Status)
	}
	objectVersionHeader := headResponse.Header.Get(s3VersionFetcher.VersionHeaderName)
	objectVersion, err := strconv.ParseInt(objectVersionHeader, 10, 64)
	if err != nil {
		return nil, err
	}
	return &StorageState{
		objectNotFound:  false,
		version:         int(objectVersion),
		storageEndpoint: s3Client.S3Endpoint,
	}, nil
}
