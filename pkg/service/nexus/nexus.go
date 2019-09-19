package nexus

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dchest/uniuri"
	"github.com/epmd-edp/nexus-operator/v2/pkg/apis/edp/v1alpha1"
	"github.com/epmd-edp/nexus-operator/v2/pkg/client/nexus"
	"github.com/epmd-edp/nexus-operator/v2/pkg/helper"
	nexusDefaultSpec "github.com/epmd-edp/nexus-operator/v2/pkg/service/nexus/spec"
	keycloakV1Api "github.com/epmd-edp/keycloak-operator/pkg/apis/v1/v1alpha1"
	"github.com/epmd-edp/nexus-operator/v2/pkg/service/platform"
	"github.com/operator-framework/operator-sdk/pkg/k8sutil"
	"github.com/pkg/errors"
	coreV1Api "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
)

var log = logf.Log.WithName("nexus_service")

const (
	//NexusDefaultConfigurationDirectoryPath
	NexusDefaultConfigurationDirectoryPath = "/usr/local/configs/default-configuration"

	//NexusDefaultScriptsPath - default scripts for uploading to Nexus
	NexusDefaultScriptsPath = "/usr/local/configs/scripts"

	LocalConfigsRelativePath = "configs"
)

// NexusService interface for Nexus EDP component
type NexusService interface {
	Install(instance v1alpha1.Nexus) (*v1alpha1.Nexus, error)
	Configure(instance v1alpha1.Nexus) (*v1alpha1.Nexus, bool, error)
	ExposeConfiguration(instance v1alpha1.Nexus) (*v1alpha1.Nexus, error)
	Integration(instance v1alpha1.Nexus) (*v1alpha1.Nexus, error)
	IsDeploymentConfigReady(instance v1alpha1.Nexus) (bool, error)
}

// NewNexusService function that returns NexusService implementation
func NewNexusService(platformService platform.PlatformService, k8sClient client.Client) NexusService {
	return NexusServiceImpl{platformService: platformService, k8sClient: k8sClient}
}

// NexusServiceImpl struct fo Nexus EDP Component
type NexusServiceImpl struct {
	platformService platform.PlatformService
	k8sClient       client.Client
	nexusClient     nexus.NexusClient
}

// IsDeploymentConfigReady check if DC for Nexus is ready
func (n NexusServiceImpl) IsDeploymentConfigReady(instance v1alpha1.Nexus) (bool, error) {
	nexusIsReady := false
	nexusDc, err := n.platformService.GetDeploymentConfig(instance)
	if err != nil {
		return nexusIsReady, helper.LogErrorAndReturn(err)
	}
	if nexusDc.Status.AvailableReplicas == 1 {
		nexusIsReady = true
	}
	return nexusIsReady, nil
}

func (n NexusServiceImpl) getNexusRestApiUrl(instance v1alpha1.Nexus) (string, error) {
	nexusApiUrl := fmt.Sprintf("http://%v.%v:%v/%v", instance.Name, instance.Namespace, nexusDefaultSpec.NexusPort, nexusDefaultSpec.NexusRestApiUrlPath)
	if _, err := k8sutil.GetOperatorNamespace(); err != nil && err == k8sutil.ErrNoNamespace {
		nexusRoute, nexusRouteScheme, err := n.platformService.GetRoute(instance.Namespace, instance.Name)
		if err != nil {
			return "", errors.Wrapf(err, "Failed to get Route for %v/%v", instance.Namespace, instance.Name)
		}
		nexusApiUrl = fmt.Sprintf("%v://%v/%v", nexusRouteScheme, nexusRoute.Spec.Host, nexusDefaultSpec.NexusRestApiUrlPath)
	}
	return nexusApiUrl, nil
}

func (n NexusServiceImpl) getNexusAdminPassword(instance v1alpha1.Nexus) (string, error) {
	secretName := fmt.Sprintf("%v-admin-password", instance.Name)
	nexusAdminCredentials, err := n.platformService.GetSecretData(instance.Namespace, secretName)
	if err != nil {
		return "", errors.Wrapf(err, "Failed to get Secret %v for %v/%v", secretName, instance.Namespace, instance.Name)
	}
	return string(nexusAdminCredentials["password"]), nil
}

func (n NexusServiceImpl) setAnnotation(instance *v1alpha1.Nexus, key string, value string) {
	if len(instance.Annotations) == 0 {
		instance.ObjectMeta.Annotations = map[string]string{
			key: value,
		}
	} else {
		instance.ObjectMeta.Annotations[key] = value
	}
}

// Integration performs integration Nexus with other EDP components
func (n NexusServiceImpl) Integration(instance v1alpha1.Nexus) (*v1alpha1.Nexus, error) {

	if instance.Spec.KeycloakSpec.Enabled {
		keycloakSecretName := fmt.Sprintf("%v-%v", instance.Name, nexusDefaultSpec.IdentityServiceCredentialsSecretPostfix)

		keycloakSecretData, err := n.platformService.GetSecretData(instance.Namespace, keycloakSecretName)
		if err != nil {
			return &instance, errors.Wrap(err, "Failed to get Keycloak client data!")
		}

		err = n.platformService.AddKeycloakProxyToDeployConf(instance, keycloakSecretData)
		if err != nil {
			return &instance, errors.Wrap(err, "Failed to add Keycloak proxy!")
		}

		keyCloakProxyPort := coreV1Api.ServicePort{
			Name:       "keycloak-proxy",
			Port:       nexusDefaultSpec.NexusKeycloakProxyPort,
			Protocol:   coreV1Api.ProtocolTCP,
			TargetPort: intstr.IntOrString{IntVal: nexusDefaultSpec.NexusKeycloakProxyPort},
		}
		if err = n.platformService.AddPortToService(instance, keyCloakProxyPort); err != nil {
			return &instance, errors.Wrap(err, "Failed to add Keycloak proxy port to service")
		}

		if err = n.platformService.UpdateRouteTarget(instance, intstr.IntOrString{IntVal: nexusDefaultSpec.NexusKeycloakProxyPort}); err != nil {
			return &instance, errors.Wrap(err, "Failed to update target port in Route")
		}
	} else {
		log.V(1).Info("Keycloak integration not enabled.")
	}

	return &instance, nil
}

// ExposeConfiguration performs exposing Nexus configuration for other EDP components
func (n NexusServiceImpl) ExposeConfiguration(instance v1alpha1.Nexus) (*v1alpha1.Nexus, error) {
	nexusApiUrl, err := n.getNexusRestApiUrl(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to get Nexus REST API URL %v/%v", instance.Namespace, instance.Name)
	}

	nexusPassword, err := n.getNexusAdminPassword(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to get Nexus admin password from secret for %v/%v", instance.Namespace, instance.Name)
	}

	err = n.nexusClient.InitNewRestClient(&instance, nexusApiUrl, nexusDefaultSpec.NexusDefaultAdminUser, nexusPassword)
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to initialize Nexus client for %v/%v", instance.Namespace, instance.Name)
	}

	nexusDefaultUsersToCreate, err := n.platformService.GetConfigMapData(instance.Namespace, fmt.Sprintf("%v-%v", instance.Name, nexusDefaultSpec.NexusDefaultUsersConfigMapPrefix))
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to get default tasks from Config Map for %v/%v", instance.Namespace, instance.Name)
	}

	var newUserSecretName string
	var parsedUsers []map[string]interface{}
	err = json.Unmarshal([]byte(nexusDefaultUsersToCreate[nexusDefaultSpec.NexusDefaultUsersConfigMapPrefix]), &parsedUsers)

	newUser := map[string][]byte{}

	for _, userProperties := range parsedUsers {
		newUser["username"] = []byte(userProperties["username"].(string))
		newUser["first_name"] = []byte(userProperties["first_name"].(string))
		newUser["last_name"] = []byte(userProperties["last_name"].(string))
		newUser["password"] = []byte(uniuri.New())
		newUserSecretName = fmt.Sprintf("%s-%s", instance.Name, newUser["username"])

		err = n.platformService.CreateSecret(instance, newUserSecretName, newUser)
		if err != nil {
			return &instance, errors.Wrapf(err, "Failed to create %s secret!", newUserSecretName)
		}

		err := n.platformService.CreateJenkinsServiceAccount(instance.Namespace, newUserSecretName)
		if err != nil {
			return &instance, errors.Wrapf(err, "Failed to create Jenkins service account %s", newUserSecretName)
		}

		data, err := n.platformService.GetSecretData(instance.Namespace, newUserSecretName)
		if err != nil {
			return &instance, errors.Wrap(err, "Failed to get CI user credentials!")
		}

		userProperties["password"] = string(data["password"])

		_, err = n.nexusClient.RunScript("setup-user", userProperties)
		if err != nil {
			return &instance, errors.Wrapf(err, "Failed to create user %v for %v/%v", userProperties["username"], instance.Namespace, instance.Name)
		}
	}

	_ = n.k8sClient.Update(context.TODO(), &instance)

	if instance.Spec.KeycloakSpec.Enabled {
		routeObject, scheme, err := n.platformService.GetRoute(instance.Namespace, instance.Name)
		if err != nil {
			return &instance, errors.Wrap(err, "Failed to get route from cluster!")
		}

		webUrl := fmt.Sprintf("%s://%s", scheme, routeObject.Spec.Host)
		keycloakClient := keycloakV1Api.KeycloakClient{}
		keycloakClient.Name = instance.Name
		keycloakClient.Namespace = instance.Namespace
		keycloakClient.Spec.ClientId = instance.Name
		keycloakClient.Spec.Public = true
		keycloakClient.Spec.WebUrl = webUrl

		err = n.platformService.CreateKeycloakClient(&keycloakClient)
		if err != nil {
			return &instance, nil
		}
	}
	return &instance, nil
}

// Configure performs self-configuration of Nexus
func (n NexusServiceImpl) Configure(instance v1alpha1.Nexus) (*v1alpha1.Nexus, bool, error) {
	nexusApiUrl, err := n.getNexusRestApiUrl(instance)
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to get Nexus REST API URL %v/%v", instance.Namespace, instance.Name)
	}

	nexusPassword, err := n.getNexusAdminPassword(instance)
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to get Nexus admin password from secret for %v/%v", instance.Namespace, instance.Name)
	}

	err = n.nexusClient.InitNewRestClient(&instance, nexusApiUrl, nexusDefaultSpec.NexusDefaultAdminUser, nexusPassword)
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to initialize Nexus client for %v/%v", instance.Namespace, instance.Name)
	}

	if nexusApiIsReady, _, err := n.nexusClient.IsNexusRestApiReady(); err != nil {
		return &instance, false, errors.Wrapf(err, "Checking if Nexus REST API for %v/%v object is ready has been failed", instance.Namespace, instance.Name)
	} else if !nexusApiIsReady {
		log.Info(fmt.Sprintf("Nexus REST API for %v/%v object is not ready for configuration yet", instance.Namespace, instance.Name))
		return &instance, false, nil
	}

	nexusDefaultScriptsToCreate, err := n.platformService.GetConfigMapData(instance.Namespace, fmt.Sprintf("%v-%v", instance.Name, nexusDefaultSpec.NexusDefaultScriptsConfigMapPrefix))
	if err != nil {
		return &instance, false, errors.Wrap(err, "Failed to get default tasks from Config Map")
	}

	err = n.nexusClient.DeclareDefaultScripts(nexusDefaultScriptsToCreate)
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to upload default scripts for %v/%v", instance.Namespace, instance.Name)
	}

	defaultScriptsAreDeclared, err := n.nexusClient.AreDefaultScriptsDeclared(nexusDefaultScriptsToCreate)
	if !defaultScriptsAreDeclared || err != nil {
		return &instance, false, errors.Wrapf(err, "Default scripts for %v/%v are not uploaded yet", instance.Namespace, instance.Name)
	}

	if nexusPassword == nexusDefaultSpec.NexusDefaultAdminPassword {
		updatePasswordParameters := map[string]interface{}{"new_password": uniuri.New()}

		nexusAdminPassword, err := n.platformService.GetSecret(instance.Namespace, instance.Name+"-admin-password")
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed to get Nexus admin secret to update!")
		}

		nexusAdminPassword.Data["password"] = []byte(updatePasswordParameters["new_password"].(string))
		err = n.platformService.UpdateSecret(nexusAdminPassword)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed to update Nexus admin secret with new pasword!")
		}

		_, err = n.nexusClient.RunScript("update-admin-password", updatePasswordParameters)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed update admin password for %v/%v", instance.Namespace, instance.Name)
		}

		passwordString := string(nexusAdminPassword.Data["password"])

		err = n.nexusClient.InitNewRestClient(&instance, nexusApiUrl, nexusDefaultSpec.NexusDefaultAdminUser, passwordString)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed to initialize Nexus client for %v/%v", instance.Namespace, instance.Name)
		}
	}
	nexusDefaultTasksToCreate, err := n.platformService.GetConfigMapData(instance.Namespace, fmt.Sprintf("%v-%v", instance.Name, nexusDefaultSpec.NexusDefaultTasksConfigMapPrefix))
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to get default tasks from Config Map for %v/%v", instance.Namespace, instance.Name)
	}

	var parsedTasks []map[string]interface{}
	err = json.Unmarshal([]byte(nexusDefaultTasksToCreate[nexusDefaultSpec.NexusDefaultTasksConfigMapPrefix]), &parsedTasks)
	for _, taskParameters := range parsedTasks {
		_, err = n.nexusClient.RunScript("create-task", taskParameters)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed to create task %v for %v/%v", taskParameters["name"], instance.Namespace, instance.Name)
		}
	}

	var emptyParameter map[string]interface{}
	_, err = n.nexusClient.RunScript("disable-outreach-capability", emptyParameter)
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to run disable-outreach-capability scripts for %v/%v", instance.Namespace, instance.Name)
	}

	nexusCapabilities, err := n.platformService.GetConfigMapData(instance.Namespace, fmt.Sprintf("%v-%v", instance.Name, "default-capabilities"))
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to get default tasks from Config Map for %v/%v", instance.Namespace, instance.Name)
	}

	var nexusParsedCapabilities []map[string]interface{}
	err = json.Unmarshal([]byte(nexusCapabilities["default-capabilities"]), &nexusParsedCapabilities)

	for _, capability := range nexusParsedCapabilities {
		_, err = n.nexusClient.RunScript("setup-capability", capability)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed to install default capabilities for %v/%v", instance.Namespace, instance.Name)
		}
	}

	enabledRealms := []map[string]interface{}{
		{"name": "NuGetApiKey"},
	}
	for _, realmName := range enabledRealms {
		_, err = n.nexusClient.RunScript("enable-realm", realmName)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed enable %v for %v/%v", enabledRealms, instance.Namespace, instance.Name)
		}
	}

	nexusDefaultRolesToCreate, err := n.platformService.GetConfigMapData(instance.Namespace, fmt.Sprintf("%v-%v", instance.Name, nexusDefaultSpec.NexusDefaultRolesConfigMapPrefix))
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to get default roles from Config Map for %v/%v", instance.Namespace, instance.Name)
	}

	var parsedRoles []map[string]interface{}
	err = json.Unmarshal([]byte(nexusDefaultRolesToCreate[nexusDefaultSpec.NexusDefaultRolesConfigMapPrefix]), &parsedRoles)
	for _, roleParameters := range parsedRoles {
		_, err := n.nexusClient.RunScript("setup-role", roleParameters)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed to create role %v for %v/%v", roleParameters["name"], instance.Namespace, instance.Name)
		}
	}

	// Creating blob storage configuration from config map
	blobsConfig, err := n.platformService.GetConfigMapData(instance.Namespace, fmt.Sprintf("%v-blobs", instance.Name))
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to get data from ConfigMap %v-blobs", instance.Name)
	}

	var parsedBlobsConfig []map[string]interface{}
	err = json.Unmarshal([]byte(blobsConfig["blobs"]), &parsedBlobsConfig)
	if err != nil {
		return &instance, false, errors.Wrap(err, "Failed to unmarshal blob ConfigMap")
	}

	for _, blob := range parsedBlobsConfig {
		_, err := n.nexusClient.RunScript("create-blobstore", blob)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed to create blob store %v!", blob["name"])
		}
	}

	// Creating repositoriesToCreate from config map
	reposToCreate, err := n.platformService.GetConfigMapData(instance.Namespace, fmt.Sprintf("%v-%v", instance.Name, nexusDefaultSpec.NexusDefaultReposToCreateConfigMapPrefix))
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to get data from ConfigMap %v-%v", instance.Name, nexusDefaultSpec.NexusDefaultReposToCreateConfigMapPrefix)
	}

	var parsedReposToCreate []map[string]interface{}
	err = json.Unmarshal([]byte(reposToCreate[nexusDefaultSpec.NexusDefaultReposToCreateConfigMapPrefix]), &parsedReposToCreate)
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to unmarshal %v-%v ConfigMap!", instance.Name, nexusDefaultSpec.NexusDefaultReposToCreateConfigMapPrefix)
	}

	for _, repositoryToCreate := range parsedReposToCreate {
		repositoryName := repositoryToCreate["name"].(string)
		repositoryType := repositoryToCreate["repositoryType"].(string)
		_, err := n.nexusClient.RunScript(fmt.Sprintf("create-repo-%v", repositoryType), repositoryToCreate)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed to create repository %v!", repositoryName)
		}
	}

	reposToDelete, err := n.platformService.GetConfigMapData(instance.Namespace, fmt.Sprintf("%v-%v", instance.Name, nexusDefaultSpec.NexusDefaultReposToDeleteConfigMapPrefix))
	if err != nil {
		return &instance, false, errors.Wrapf(err, " Failed to get data from ConfigMap %v-%v", instance.Name, nexusDefaultSpec.NexusDefaultReposToDeleteConfigMapPrefix)
	}

	var parsedReposToDelete []map[string]interface{}
	err = json.Unmarshal([]byte(reposToDelete[nexusDefaultSpec.NexusDefaultReposToDeleteConfigMapPrefix]), &parsedReposToDelete)
	if err != nil {
		return &instance, false, errors.Wrapf(err, "Failed to unmarshal %v-%v ConfigMap!", instance.Name, nexusDefaultSpec.NexusDefaultReposToDeleteConfigMapPrefix)
	}

	for _, repositoryToDelete := range parsedReposToDelete {
		_, err := n.nexusClient.RunScript("delete-repo", repositoryToDelete)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed to delete repository %v", repositoryToDelete)
		}
	}

	for _, user := range instance.Spec.Users {
		setupUserParameters := map[string]interface{}{
			"username":   user.Username,
			"first_name": user.FirstName,
			"last_name":  user.LastName,
			"email":      user.Email,
			"password":   uniuri.New(),
			"roles":      user.Roles,
		}

		_, err = n.nexusClient.RunScript("setup-user", setupUserParameters)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "Failed to create user %v", user.Username, instance.Namespace, instance.Name)
		}
	}

	return &instance, true, nil
}

// Install performs installation of Nexus
func (n NexusServiceImpl) Install(instance v1alpha1.Nexus) (*v1alpha1.Nexus, error) {

	adminSecret := map[string][]byte{
		"user":     []byte(nexusDefaultSpec.NexusDefaultAdminUser),
		"password": []byte(nexusDefaultSpec.NexusDefaultAdminPassword),
	}

	err := n.platformService.CreateSecret(instance, instance.Name+"-admin-password", adminSecret)
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to Secret for %v/%v", instance.Namespace, instance.Name)
	}

	err = n.platformService.CreateVolume(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to create Volume for %v/%v", instance.Namespace, instance.Name)
	}

	_, err = n.platformService.CreateServiceAccount(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to create Service Account for %v/%v", instance.Namespace, instance.Name)
	}

	err = n.platformService.CreateService(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to create Service for %v/%v", instance.Namespace, instance.Name)
	}

	executableFilePath := helper.GetExecutableFilePath()
	NexusConfigurationDirectoryPath := NexusDefaultConfigurationDirectoryPath
	if _, err = k8sutil.GetOperatorNamespace(); err != nil && err == k8sutil.ErrNoNamespace {
		NexusConfigurationDirectoryPath = fmt.Sprintf("%v/../%v/default-configuration", executableFilePath, LocalConfigsRelativePath)
	}
	err = n.platformService.CreateConfigMapsFromDirectory(instance, NexusConfigurationDirectoryPath, true)
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to create default Config Maps for configuration %v/%v", instance.Namespace, instance.Name)
	}

	NexusScriptsPath := NexusDefaultScriptsPath
	if _, err = k8sutil.GetOperatorNamespace(); err != nil && err == k8sutil.ErrNoNamespace {
		NexusScriptsPath = fmt.Sprintf("%v/../%v/scripts", executableFilePath, LocalConfigsRelativePath)
	}
	err = n.platformService.CreateConfigMapsFromDirectory(instance, NexusScriptsPath, false)
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to create default Config Maps for scripts for %v/%v", instance.Namespace, instance.Name)
	}

	err = n.platformService.CreateDeployConf(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to create Deployment Config for %v/%v", instance.Namespace, instance.Name)
	}

	err = n.platformService.CreateExternalEndpoint(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "Failed to create External Route for %v/%v", instance.Namespace, instance.Name)
	}

	return &instance, nil
}
