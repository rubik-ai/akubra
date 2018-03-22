package config

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	httphandlerconfig "github.com/allegro/akubra/httphandler/config"
	regionsconfig "github.com/allegro/akubra/regions/config"
	"github.com/allegro/akubra/storages/auth"
	"github.com/allegro/akubra/storages/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/validator.v1"
)

// import (
// 	"testing"

// 	"net/http"
// 	"net/http/httptest"

// 	shardingconfig "github.com/allegro/akubra/sharding/config"
// 	validator "gopkg.in/validator.v1"

// 	"errors"

// 	"github.com/stretchr/testify/assert"
// )

type CustomItemsTestUnique struct {
	Items []string `validate:"UniqueValuesSlice=Items"`
}

type CustomItemsTestNoEmpty struct {
	Items []string `validate:"NoEmptyValuesSlice=Items"`
}

func TestShouldValidateWhenValuesInSliceAreUnique(t *testing.T) {
	var data CustomItemsTestUnique
	data.Items = []string{"item001", "item002"}

	err := validator.SetValidationFunc("UniqueValuesSlice", UniqueValuesInSliceValidator)
	require.NoError(t, err)
	valid, _ := validator.Validate(data)

	assert.True(t, valid, "Should be true")
}

func TestShouldNotValidateWhenValuesInSliceAreDuplicated(t *testing.T) {
	var data CustomItemsTestUnique
	data.Items = []string{"not_unique", "not_unique"}

	err := validator.SetValidationFunc("UniqueValuesSlice", UniqueValuesInSliceValidator)
	require.NoError(t, err)
	valid, validationErrors := validator.Validate(data)

	assert.Contains(t, validationErrors, "Items")
	assert.False(t, valid, "Should be false")
}

func TestShouldValidateWhenValuesInSliceAreNoEmpty(t *testing.T) {
	var data CustomItemsTestNoEmpty
	data.Items = []string{"i1", "i2"}

	err := validator.SetValidationFunc("NoEmptyValuesSlice", NoEmptyValuesInSliceValidator)
	require.NoError(t, err)

	valid, _ := validator.Validate(data)

	assert.True(t, valid, "Should be true")
}

func TestShouldNotValidateWhenValuesInSliceAreEmpty(t *testing.T) {
	var data CustomItemsTestNoEmpty
	data.Items = []string{"value", "  "}

	err := validator.SetValidationFunc("NoEmptyValuesSlice", NoEmptyValuesInSliceValidator)
	require.NoError(t, err)

	valid, validationErrors := validator.Validate(data)

	assert.Contains(t, validationErrors, "Items")
	assert.False(t, valid, "Should be false")
}

func TestShouldPassListenPortsLogicalValidator(t *testing.T) {
	listen := ":8080"
	listenTechnicalEndpoint := ":8081"
	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048
	regionConfig := regionsconfig.Region{}
	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81", listen,
		listenTechnicalEndpoint,
		map[string]regionsconfig.Region{"region": regionConfig})
	valid, validationErrors := yamlConfig.ListenPortsLogicalValidator()

	assert.Len(t, validationErrors, 0, "Should not be errors")
	assert.True(t, valid, "Should be true")
}

func TestShouldNotPassListenPortsLogicalValidatorWhenPortsAreEqual(t *testing.T) {
	listen := "127.0.0.1:8080"
	listenTechnicalEndpoint := listen
	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048
	regionConfig := regionsconfig.Region{}
	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81", listen,
		listenTechnicalEndpoint,
		map[string]regionsconfig.Region{"region": regionConfig})
	valid, validationErrors := yamlConfig.ListenPortsLogicalValidator()

	assert.Len(t, validationErrors, 1, "Should be one error")
	assert.False(t, valid, "Should be false")
}

func TestShouldPassHeaderContentLengthValidator(t *testing.T) {
	var bodySizeLimit int64 = 128
	request := httptest.NewRequest("POST", "http://somepath", nil)
	request.Header.Set("Content-Length", "128")
	result := httphandlerconfig.RequestHeaderContentLengthValidator(*request, bodySizeLimit)
	assert.Equal(t, 0, result)
}

func TestShouldNotPassHeaderContentLengthValidatorAndReturnEntityTooLargeCode(t *testing.T) {
	var bodySizeLimit int64 = 1024
	request := httptest.NewRequest("POST", "http://somepath", nil)
	request.Header.Set("Content-Length", "1025")
	result := httphandlerconfig.RequestHeaderContentLengthValidator(*request, bodySizeLimit)
	assert.Equal(t, http.StatusRequestEntityTooLarge, result)
}

func TestShouldNotPassHeaderContentLengthValidatorAndReturnBadRequestOnUnparsableContentLengthHeader(t *testing.T) {
	var bodySizeLimit int64 = 64
	request := httptest.NewRequest("POST", "http://somepath", nil)
	request.Header.Set("Content-Length", "strange-content-header")
	result := httphandlerconfig.RequestHeaderContentLengthValidator(*request, bodySizeLimit)
	assert.Equal(t, http.StatusBadRequest, result)
}

func TestShouldPassRequestHeaderContentTypeValidator(t *testing.T) {
	requiredContentType := "application/yaml"
	request := httptest.NewRequest("POST", "http://somepath", nil)
	request.Header.Set("Content-Type", "application/yaml")
	result := RequestHeaderContentTypeValidator(*request, requiredContentType)
	assert.Equal(t, 0, result)
}

func TestShouldNotPassRequestHeaderContentTypeValidatorWhenContentTypeIsEmpty(t *testing.T) {
	requiredContentType := "application/yaml"
	request := httptest.NewRequest("POST", "http://somepath", nil)
	request.Header.Set("Content-Type", "")
	result := RequestHeaderContentTypeValidator(*request, requiredContentType)
	assert.Equal(t, http.StatusBadRequest, result)
}

func TestShouldNotPassRequestHeaderContentTypeValidatorWhenContentTypeIsUnsupported(t *testing.T) {
	requiredContentType := "application/yaml"
	request := httptest.NewRequest("POST", "http://somepath", nil)
	request.Header.Set("Content-Type", "application/json")
	result := RequestHeaderContentTypeValidator(*request, requiredContentType)
	assert.Equal(t, http.StatusUnsupportedMediaType, result)
}

func TestValidatorShouldPassWithValidRegionConfig(t *testing.T) {
	multiClusterConfig := regionsconfig.RegionCluster{
		Name:   "cluster1test",
		Weight: 1,
	}

	regionConfig := regionsconfig.Region{
		Clusters: []regionsconfig.RegionCluster{multiClusterConfig},
		Domains:  []string{"domain.dc"},
	}

	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048

	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81",
		"127.0.0.1:1234", "127.0.0.1:1235",
		map[string]regionsconfig.Region{"region": regionConfig})

	valid, validationErrors := yamlConfig.RegionsEntryLogicalValidator()
	assert.True(t, valid)
	assert.Empty(t, validationErrors)
}

func TestValidatorShouldFailWithMissingCluster(t *testing.T) {
	multiClusterConfig := regionsconfig.RegionCluster{
		Name:   "someothercluster",
		Weight: 1,
	}

	regionConfig := regionsconfig.Region{
		Clusters: []regionsconfig.RegionCluster{multiClusterConfig},
		Domains:  []string{"domain.dc"},
	}
	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048
	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81",
		"127.0.0.1:1234", "127.0.0.1:1235",
		map[string]regionsconfig.Region{"testregion": regionConfig})
	valid, validationErrors := yamlConfig.RegionsEntryLogicalValidator()
	assert.False(t, valid)
	assert.Equal(
		t,
		errors.New("Cluster \"testregion\" is region \"someothercluster\" is not defined"),
		validationErrors["RegionsEntryLogicalValidator"][0])
}

func TestValidatorShouldFailWithInvalidWeight(t *testing.T) {
	multiClusterConfig := regionsconfig.RegionCluster{
		Name:   "cluster1test",
		Weight: 199,
	}
	regionConfig := regionsconfig.Region{
		Clusters: []regionsconfig.RegionCluster{multiClusterConfig},
		Domains:  []string{"domain.dc"},
	}
	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048
	regions := map[string]regionsconfig.Region{"testregion": regionConfig}
	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81",
		"127.0.0.1:1234", "127.0.0.1:1235", regions)

	valid, validationErrors := yamlConfig.RegionsEntryLogicalValidator()
	assert.False(t, valid)
	assert.Equal(
		t,
		errors.New("Weight for cluster \"cluster1test\" in region \"testregion\" is not valid"),
		validationErrors["RegionsEntryLogicalValidator"][0])
}

func TestValidatorShouldFailWithMissingClusterDomain(t *testing.T) {
	multiClusterConfig := regionsconfig.RegionCluster{
		Name:   "cluster1test",
		Weight: 1,
	}
	regionConfig := regionsconfig.Region{
		Clusters: []regionsconfig.RegionCluster{multiClusterConfig},
	}
	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048

	regions := map[string]regionsconfig.Region{"testregion": regionConfig}
	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81", "127.0.0.1:1234", "127.0.0.1:1235", regions)
	valid, validationErrors := yamlConfig.RegionsEntryLogicalValidator()
	assert.False(t, valid)
	assert.Equal(
		t,
		errors.New("No domain defined for region \"testregion\""),
		validationErrors["RegionsEntryLogicalValidator"][0])
}

func TestValidatorShouldFailWithMissingClusterDefinition(t *testing.T) {
	regionConfig := regionsconfig.Region{
		Domains: []string{"domain.dc"},
	}
	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048
	regions := map[string]regionsconfig.Region{"testregion": regionConfig}
	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81", "127.0.0.1:1234", "127.0.0.1:1235", regions)
	valid, validationErrors := yamlConfig.RegionsEntryLogicalValidator()
	assert.False(t, valid)
	assert.Equal(
		t,
		errors.New("No clusters defined for region \"testregion\""),
		validationErrors["RegionsEntryLogicalValidator"][0])
}

func TestValidatorShouldFailWhenADomainContainingOtherDomainAsSubDomainIsDefined(t *testing.T) {
	regionConfig := regionsconfig.Region{
		Domains: []string{"domain.dc", "other.domain.dc"},
	}
	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048
	regions := map[string]regionsconfig.Region{"testregion": regionConfig}
	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81", "127.0.0.1:1234", "127.0.0.1:1235", regions)
	valid, validationErrors := yamlConfig.DomainsEntryLogicalValidator()
	assert.False(t, valid)
	assert.Equal(
		t,
		errors.New("Invalid domain 'other.domain.dc', it conflicts with 'domain.dc'"),
		validationErrors["DomainsEntryLogicalValidator"][0])
}

func TestValidatorShouldFailWithWrongDomainDeclarationOrder(t *testing.T) {
	regionConfig := regionsconfig.Region{
		Domains: []string{"other.domain.dc", "domain.dc"},
	}
	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048
	regions := map[string]regionsconfig.Region{"testregion": regionConfig}
	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81", "127.0.0.1:1234", "127.0.0.1:1235", regions)
	valid, validationErrors := yamlConfig.DomainsEntryLogicalValidator()
	assert.False(t, valid)
	assert.Equal(
		t,
		errors.New("Invalid domain 'domain.dc', it conflicts with 'other.domain.dc'"),
		validationErrors["DomainsEntryLogicalValidator"][0])
}

func TestValidatorShouldFailWithADomainContainingOtherDomainIsDefinedInDifferentRegions(t *testing.T) {
	regionConfig := regionsconfig.Region{
		Domains: []string{"domain.dc"}
	}
	regionConfig1 := regionsconfig.Region{
		Domains: []string{"other.domain.dc"},
	}

	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048
	regions := map[string]regionsconfig.Region{
		"testregion":  regionConfig,
		"testregion1": regionConfig1,
	}
	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81", "127.0.0.1:1234", "127.0.0.1:1235", regions)
	valid, validationErrors := yamlConfig.DomainsEntryLogicalValidator()
	assert.False(t, valid)
	assert.Equal(
		t,
		errors.New("Invalid domain 'other.domain.dc', it conflicts with 'domain.dc'"),
		validationErrors["DomainsEntryLogicalValidator"][0])
}

func TestValidatorShouldFailWhenDomainIsUsedMultipleTimes(t *testing.T) {
	regionConfig := regionsconfig.Region{
		Domains: []string{"domain.dc"},
	}
	regionConfig1 := regionsconfig.Region{
		Domains: []string{"domain.dc"},
	}

	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048
	regions := map[string]regionsconfig.Region{
		"testregion":  regionConfig,
		"testregion1": regionConfig1,
	}
	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81", "127.0.0.1:1234", "127.0.0.1:1235", regions)
	valid, validationErrors := yamlConfig.DomainsEntryLogicalValidator()
	assert.False(t, valid)
	assert.Equal(
		t,
		errors.New("Invalid domain 'domain.dc', it conflicts with 'domain.dc'"),
		validationErrors["DomainsEntryLogicalValidator"][0])
}

func TestValidatorShouldPassWithProperDomainsDefined(t *testing.T) {
	regionConfig := regionsconfig.Region{
		Domains: []string{"domain.dc", "sub.domain.dc2"},
	}
	regionConfig1 := regionsconfig.Region{
		Domains: []string{"other-domain.dc"},
	}

	var size httphandlerconfig.HumanSizeUnits
	size.SizeInBytes = 2048
	regions := map[string]regionsconfig.Region{
		"testregion":  regionConfig,
		"testregion1": regionConfig1,
	}
	yamlConfig := PrepareYamlConfig(size, 31, 45, "127.0.0.1:81", "127.0.0.1:1234", "127.0.0.1:1235", regions)
	valid, validationErrors := yamlConfig.DomainsEntryLogicalValidator()
	assert.True(t, valid)
	assert.Empty(
		t,
		validationErrors["DomainsEntryLogicalValidator"])
}

func TestValidatorShouldFailWhenPathStyleBackendHasNoSecretKeyOrAccessKeyProvidedOrIsOfWrongType(t *testing.T) {
	backendProperties := make(map[string]string, 1)
	backendProperties["AccessKey"] = "123a"
	backendWithoutSecret := config.Backend{
		Properties:     backendProperties,
		ForcePathStyle: true,
		Type:           auth.S3FixedKey,
	}
	backendProperties2 := make(map[string]string, 1)
	backendProperties2["Secret"] = "32"
	backendWithoutAccessKey := config.Backend{
		Properties:     backendProperties2,
		ForcePathStyle: true,
		Type:           auth.S3FixedKey,
	}
	backendProperties3 := make(map[string]string, 2)
	backendWithWrongType := config.Backend{
		Properties:     backendProperties3,
		ForcePathStyle: true,
		Type:           auth.Passthrough,
	}
	backendProperties4 := make(map[string]string, 0)
	backendWithoutAuthEndpoint := config.Backend{
		Properties:     backendProperties4,
		ForcePathStyle: true,
		Type:           auth.S3AuthService,
	}

	invalidBackendsMap := make(map[string]config.Backend, 4)
	invalidBackendsMap["backend1"] = backendWithoutSecret
	invalidBackendsMap["backend2"] = backendWithoutAccessKey
	invalidBackendsMap["backend3"] = backendWithWrongType
	invalidBackendsMap["backend4"] = backendWithoutAuthEndpoint
	yamlConfig := YamlConfig{
		Backends: invalidBackendsMap,
	}

	valid, validationErrors := yamlConfig.BackendsLogicalValidator()

	assert.Len(t, validationErrors["BackendsLogicalValidator"], 4)
	assert.False(t, valid)
	assert.Contains(
		t,
		validationErrors["BackendsLogicalValidator"],
		fmt.Errorf("Backend backend1 has ForcePathStyle turned on, but it's missing Secret"))
	assert.Contains(
		t,
		validationErrors["BackendsLogicalValidator"],
		fmt.Errorf("Backend backend2 has ForcePathStyle turned on, but it's missing AccessKey"))
	assert.Contains(
		t,
		validationErrors["BackendsLogicalValidator"],
		fmt.Errorf("Backend backend3 has ForcePathStyle turned on, but it's of wrong type"))
	assert.Contains(
		t,
		validationErrors["BackendsLogicalValidator"],
		fmt.Errorf("Backend backend4 has ForcePathStyle turned on, but it's missing AuthServiceEndpoint"))
}

func TestValidatorShouldPassWhenBackendsAreDefinedProperly(t *testing.T) {
	backendProperties1 := make(map[string]string, 2)
	backendProperties1["AccessKey"] = "123a"
	backendProperties1["Secret"] = "123"
	validBackend1 := config.Backend{
		Properties:     backendProperties1,
		ForcePathStyle: true,
		Type:           auth.S3FixedKey,
	}
	backendProperties2 := make(map[string]string, 1)
	backendProperties2["AuthServiceEndpoint"] = "http://localhost/auth"
	validBackend2 := config.Backend{
		Properties:     backendProperties2,
		ForcePathStyle: true,
		Type:           auth.S3AuthService,
	}
	validBackend3 := config.Backend{
		Properties:     make(map[string]string, 0),
		ForcePathStyle: false,
		Type:           auth.Passthrough,
	}
	validBackendsMap := make(map[string]config.Backend, 3)
	validBackendsMap["backend1"] = validBackend1
	validBackendsMap["backend2"] = validBackend2
	validBackendsMap["backend2"] = validBackend3

	yamlConfig := YamlConfig{
		Backends: validBackendsMap,
	}

	valid, validationErrors := yamlConfig.BackendsLogicalValidator()

	assert.Len(t, validationErrors["BackendsLogicalValidator"], 0)
	assert.True(t, valid)
}
