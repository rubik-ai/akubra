package storages

import (
	"bytes"
	"github.com/allegro/akubra/internal/akubra/storages/config"
	"io/ioutil"
	"net/http"
	"net/url"
	"testing"

	"github.com/serialx/hashring"
	"github.com/stretchr/testify/assert"
)

func TestShouldNotBeAbleToServeTheMultiPartUploadRequestWhenBackendRingIsEmpty(testSuite *testing.T) {

	requestURL, _ := url.Parse("http://localhost:3212/someBucket/someObject?uploads")
	multiPartUploadRequest := &http.Request{URL: requestURL}
	emptyMultiPartUploadHashRing := hashring.New([]string{})
	activeBackendRoundTrippers := make(map[string]*StorageClient)

	multiPartRoundTripper := MultiPartRoundTripper{
		activeBackendRoundTrippers,
		emptyMultiPartUploadHashRing,
		nil,
	}

	respChan := multiPartRoundTripper.Do(multiPartUploadRequest)
	for resp := range respChan {
		assert.Error(testSuite, resp.Error, "can't handle multi upload")
	}
}

func TestShouldNotBeAbleToServeTheMultiPartUploadRequestWhenAllBackendsAreInMaintenance(testSuite *testing.T) {

	requestURL, _ := url.Parse("http://localhost:3212/someBucket/someObject?uploads")
	multiPartUploadRequest := &http.Request{URL: requestURL}

	maintenanceBackendURL, _ := url.Parse("http://maintenance:8421")
	hashRingOnlyWithMaitenanceBackend := hashring.New([]string{maintenanceBackendURL.String()})

	multiPartRoundTripper := MultiPartRoundTripper{
		make(map[string]*StorageClient),
		hashRingOnlyWithMaitenanceBackend,
		nil,
	}

	respChan := multiPartRoundTripper.Do(multiPartUploadRequest)
	for resp := range respChan {
		assert.Error(testSuite, resp.Error, "can't handle multi upload")
	}
}

func TestShouldDetectMultiPartUploadRequestWhenItIsAInitiateRequestOrUploadPartRequest(testSuite *testing.T) {

	initiateRequestURL, _ := url.Parse("http://localhost:3212/someBucket/someObject?uploads")
	initiateMultiPartUploadRequest := &http.Request{URL: initiateRequestURL}

	uploadPartRequestURL, _ := url.Parse("http://localhost:3212/someBucket/someObject?partNumber=1&uploadId=123")
	uploadPartRequest := &http.Request{URL: uploadPartRequestURL}

	responseForInitiate := &http.Response{Request: initiateMultiPartUploadRequest}
	responseForPartUpload := &http.Response{Request: uploadPartRequest}

	activeBackendRoundTripper1 := &MockedRoundTripper{}
	activeBackendRoundTripper2 := &MockedRoundTripper{}

	activeBackendURL, _ := url.Parse("http://active:1234")
	activeBackendURL2, _ := url.Parse("http://active2:1234")

	activateBackend1 := &StorageClient{
		Storage:      config.Storage{Maintenance: false},
		RoundTripper: activeBackendRoundTripper1,
		Endpoint:     *activeBackendURL,
		Name:         "activateBackend",
	}

	activateBackend2 := &StorageClient{
		RoundTripper: activeBackendRoundTripper2,
		Endpoint:     *activeBackendURL2,
		Storage:      config.Storage{Maintenance: false},
		Name:         "activateBackend2",
	}

	multiPartUploadHashRing := hashring.New([]string{activateBackend1.Endpoint.String(), activateBackend2.Endpoint.String()})

	activeBackendRoundTrippers := make(map[string]*StorageClient)
	activeBackendRoundTrippers[activateBackend1.Endpoint.String()] = activateBackend1
	activeBackendRoundTrippers[activateBackend2.Endpoint.String()] = activateBackend2

	multiPartRoundTripper := MultiPartRoundTripper{
		activeBackendRoundTrippers,
		multiPartUploadHashRing,
		[]string{activeBackendURL.String(), activeBackendURL2.String()},
	}

	activeBackendRoundTripper1.On("RoundTrip", initiateMultiPartUploadRequest).Return(responseForInitiate, nil)
	activeBackendRoundTripper1.On("RoundTrip", uploadPartRequest).Return(responseForPartUpload, nil)

	rChan1 := multiPartRoundTripper.Do(initiateMultiPartUploadRequest)
	for bresp := range rChan1 {
		assert.Equal(testSuite, bresp.Response, responseForInitiate)
		assert.NoError(testSuite, bresp.Error)
	}
	rChan2 := multiPartRoundTripper.Do(uploadPartRequest)
	for bresp := range rChan2 {
		assert.Equal(testSuite, bresp.Response, responseForPartUpload)
		assert.NoError(testSuite, bresp.Error)
	}

	activeBackendRoundTripper1.AssertNumberOfCalls(testSuite, "RoundTrip", 2)
	activeBackendRoundTripper2.AssertNumberOfCalls(testSuite, "RoundTrip", 0)
}

func TestShouldDetectMultiPartCompletionAndTryToNotifyTheMigratorButFailOnParsingTheResponse(testSuite *testing.T) {
	testMultipartFlow(200, "<InvalidResponse></InvalidResponse>", testSuite)
}

func TestShouldDetectMultiPartCompletionAndTryToNotifyTheMigratorWhenStatusCodeIsWrong(testSuite *testing.T) {
	testMultipartFlow(500, "<Error>Nope</Error>", testSuite)
}

func TestShouldUpdateExecutionDelayOfTheConsistencyRecordIfMultiPartWasSuccessful(t *testing.T) {
	testMultipartFlow(200, successfulMultipartResponse, t)
}

func TestShouldNotUpdateExecutionTimeIfWatchdogIsNotDefined(t *testing.T) {
	testMultipartFlow(200, successfulMultipartResponse, t)
}

func testMultipartFlow(statusCode int, xmlResponse string, testSuite *testing.T) {

	completeUploadRequestURL, _ := url.Parse("http://localhost:3212/someBucket/someObject?uploadId=321")
	completeUploadRequest := &http.Request{URL: completeUploadRequestURL}

	responseForComplete := &http.Response{Request: completeUploadRequest}
	XMLResponse := xmlResponse
	responseForComplete.Body = ioutil.NopCloser(bytes.NewBufferString(XMLResponse))
	responseForComplete.StatusCode = statusCode

	activeBackendRoundTripper1 := &MockedRoundTripper{}
	activeBackendRoundTripper2 := &MockedRoundTripper{}

	activeBackendURL, _ := url.Parse("http://active:1234")

	activateBackend1 := &StorageClient{
		RoundTripper: activeBackendRoundTripper1,
		Endpoint:     *activeBackendURL,
		Storage:      config.Storage{Maintenance: false},
		Name:         "activateBackend1",
	}

	activeBackendURL2, _ := url.Parse("http://active2:1234")

	activateBackend2 := &StorageClient{
		RoundTripper: activeBackendRoundTripper2,
		Endpoint:     *activeBackendURL2,
		Storage:      config.Storage{Maintenance: false},
		Name:         "activateBackend2",
	}

	multiPartUploadHashRing := hashring.New([]string{activateBackend1.Endpoint.String(), activateBackend2.Endpoint.String()})

	activeBackendRoundTrippers := make(map[string]*StorageClient)
	activeBackendRoundTrippers[activateBackend1.Endpoint.String()] = activateBackend1
	activeBackendRoundTrippers[activateBackend2.Endpoint.String()] = activateBackend2

	multiPartRoundTripper := MultiPartRoundTripper{
		activeBackendRoundTrippers,
		multiPartUploadHashRing,
		[]string{activeBackendURL.String(), activeBackendURL2.String()},
	}

	activeBackendRoundTripper1.On("RoundTrip", completeUploadRequest).Return(responseForComplete, nil)

	rChan := multiPartRoundTripper.Do(completeUploadRequest)
	for bresp := range rChan {
		if bresp.Response == nil {
			continue
		}
		assert.Equal(testSuite, bresp.Response, responseForComplete)
	}

	activeBackendRoundTripper1.AssertNumberOfCalls(testSuite, "RoundTrip", 1)
}

const successfulMultipartResponse = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>" +
	"<CompleteMultipartUploadResult xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">" +
	"<Location>http://Example-Bucket.s3.amazonaws.com/Example-Object</Location>" +
	"<Bucket>Example-Bucket</Bucket>" +
	"<Key>Example-Object</Key>" +
	"<ETag>\"3858f62230ac3c915f300c664312c11f-9\"</ETag>" +
	"</CompleteMultipartUploadResult>"
