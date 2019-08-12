package nexus

import (
	"encoding/json"
	"fmt"
	"github.com/operator-framework/operator-sdk/pkg/k8sutil"
	"github.com/pkg/errors"
	"log"
	"nexus-operator/pkg/apis/edp/v1alpha1"
	"nexus-operator/pkg/client/nexus"
	"nexus-operator/pkg/helper"
	nexusDefaultSpec "nexus-operator/pkg/service/nexus/spec"
	"nexus-operator/pkg/service/platform"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

// Integration performs integration Nexus with other EDP components
func (n NexusServiceImpl) Integration(instance v1alpha1.Nexus) (*v1alpha1.Nexus, error) {
	return &instance, nil
}

// ExposeConfiguration performs exposing Nexus configuration for other EDP components
func (n NexusServiceImpl) ExposeConfiguration(instance v1alpha1.Nexus) (*v1alpha1.Nexus, error) {
	return &instance, nil
}

// Configure performs self-configuration of Nexus
func (n NexusServiceImpl) Configure(instance v1alpha1.Nexus) (*v1alpha1.Nexus, bool, error) {
	nexusApiUrl := fmt.Sprintf("http://%v.%v:%v/%v", instance.Name, instance.Namespace, nexusDefaultSpec.NexusPort, nexusDefaultSpec.NexusRestApiUrlPath)
	if _, err := k8sutil.GetOperatorNamespace(); err != nil && err == k8sutil.ErrNoNamespace {
		nexusRoute, nexusRouteScheme, err := n.platformService.GetRoute(instance.Namespace, instance.Name)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "[ERROR] Failed to get route for %v/%v", instance.Namespace, instance.Name)
		}
		nexusApiUrl = fmt.Sprintf("%v://%v/%v", nexusRouteScheme, nexusRoute.Spec.Host, nexusDefaultSpec.NexusRestApiUrlPath)
	}

	err := n.nexusClient.InitNewRestClient(&instance, nexusApiUrl, nexusDefaultSpec.NexusDefaultAdminUser, nexusDefaultSpec.NexusDefaultAdminPassword)
	if err != nil {
		return &instance, false, errors.Wrapf(err, "[ERROR] Failed to initialize Nexus client for %v/%v", instance.Namespace, instance.Name)
	}

	nexusApiIsReady, err := n.nexusClient.IsNexusRestApiReady()
	if nexusApiIsReady, err = n.nexusClient.IsNexusRestApiReady(); err != nil {
		return &instance, false, errors.Wrapf(err, "[ERROR] Checking if Nexus REST API for %v/%v object is ready has been failed", instance.Namespace, instance.Name)
	} else if !nexusApiIsReady {
		log.Printf("[WARNING] Nexus REST API for %v/%v object is not ready for configuration yet", instance.Namespace, instance.Name)
		return &instance, false, nil
	}

	nexusDefaultScriptsToCreate, err := n.platformService.GetConfigMapData(instance.Namespace, fmt.Sprintf("%v-%v", instance.Name, nexusDefaultSpec.NexusDefaultScriptsConfigMapPrefix))
	if err != nil {
		return &instance, false, errors.Wrap(err, "[ERROR] Failed to get default tasks from Config Map")
	}

	err = n.nexusClient.DeclareDefaultScripts(nexusDefaultScriptsToCreate)
	if err != nil {
		return &instance, false, errors.Wrapf(err, "[ERROR] Failed to upload default scripts for %v/%v", instance.Namespace, instance.Name)
	}

	defaultScriptsAreDeclared, err := n.nexusClient.AreDefaultScriptsDeclared(nexusDefaultScriptsToCreate)
	if !defaultScriptsAreDeclared || err != nil {
		return &instance, false, errors.Wrapf(err, "[ERROR] Default scripts for %v/%v are not uploaded yet", instance.Namespace, instance.Name)
	}

	nexusDefaultTasksToCreate, err := n.platformService.GetConfigMapData(instance.Namespace, fmt.Sprintf("%v-%v", instance.Name, nexusDefaultSpec.NexusDefaultTasksConfigMapPrefix))
	if err != nil {
		return &instance, false, errors.Wrapf(err, "[ERROR] Failed to get default tasks from Config Map for %v/%v", instance.Namespace, instance.Name)
	}

	var parsedTasks []map[string]interface{}
	err = json.Unmarshal([]byte(nexusDefaultTasksToCreate[nexusDefaultSpec.NexusDefaultTasksConfigMapPrefix]), &parsedTasks)
	for _, taskParameters := range parsedTasks {
		err = n.nexusClient.CreateTask(taskParameters)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "[ERROR] Failed to create task %v for %v/%v", taskParameters["name"], instance.Namespace, instance.Name)
		}
	}

	var emptyParameter map[string]interface{}
	_, err = n.nexusClient.RunScript("disable-outreach-capability", emptyParameter)
	if err != nil {
		return &instance, false, errors.Wrapf(err, "[ERROR] Failed to run disable-outreach-capability scripts for %v/%v", instance.Namespace, instance.Name)
	}

	enabledRealms := []map[string]interface{}{
		{"name": "NuGetApiKey"},
	}
	for _, realmName := range enabledRealms {
		_, err = n.nexusClient.RunScript("enable-realm", realmName)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "[ERROR] Failed enable %v for %v/%v", enabledRealms, instance.Namespace, instance.Name)
		}
	}

	nexusDefaultRolesToCreate, err := n.platformService.GetConfigMapData(instance.Namespace, fmt.Sprintf("%v-%v", instance.Name, nexusDefaultSpec.NexusDefaultRolesConfigMapPrefix))
	if err != nil {
		return &instance, false, errors.Wrapf(err, "[ERROR] Failed to get default roles from Config Map for %v/%v", instance.Namespace, instance.Name)
	}

	var parsedRoles []map[string]interface{}
	err = json.Unmarshal([]byte(nexusDefaultRolesToCreate[nexusDefaultSpec.NexusDefaultRolesConfigMapPrefix]), &parsedRoles)
	for _, roleParameters := range parsedRoles {
		n.nexusClient.CreateRole(roleParameters)
		if err != nil {
			return &instance, false, errors.Wrapf(err, "[ERROR] Failed to create role %v for %v/%v", roleParameters["name"], instance.Namespace, instance.Name)
		}
	}

	return &instance, true, nil
}

// Install performs installation of Nexus
func (n NexusServiceImpl) Install(instance v1alpha1.Nexus) (*v1alpha1.Nexus, error) {
	err := n.platformService.CreateVolume(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "[ERROR] Failed to create Volume for %v/%v", instance.Namespace, instance.Name)
	}

	_, err = n.platformService.CreateServiceAccount(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "[ERROR] Failed to create Service Account for %v/%v", instance.Namespace, instance.Name)
	}

	err = n.platformService.CreateService(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "[ERROR] Failed to create Service for %v/%v", instance.Namespace, instance.Name)
	}

	executableFilePath := helper.GetExecutableFilePath()
	NexusConfigurationDirectoryPath := NexusDefaultConfigurationDirectoryPath
	if _, err = k8sutil.GetOperatorNamespace(); err != nil && err == k8sutil.ErrNoNamespace {
		NexusConfigurationDirectoryPath = fmt.Sprintf("%v/../%v/default-configuration", executableFilePath, LocalConfigsRelativePath)
	}
	err = n.platformService.CreateConfigMapsFromDirectory(instance, NexusConfigurationDirectoryPath, true)
	if err != nil {
		return &instance, errors.Wrapf(err, "[ERROR] Failed to create default Config Maps for configuration %v/%v", instance.Namespace, instance.Name)
	}

	NexusScriptsPath := NexusDefaultScriptsPath
	if _, err = k8sutil.GetOperatorNamespace(); err != nil && err == k8sutil.ErrNoNamespace {
		NexusScriptsPath = fmt.Sprintf("%v/../%v/scripts", executableFilePath, LocalConfigsRelativePath)
	}
	err = n.platformService.CreateConfigMapsFromDirectory(instance, NexusScriptsPath, false)
	if err != nil {
		return &instance, errors.Wrapf(err, "[ERROR] Failed to create default Config Maps for scripts for %v/%v", instance.Namespace, instance.Name)
	}

	err = n.platformService.CreateDeployConf(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "[ERROR] Failed to create Deployment Config for %v/%v", instance.Namespace, instance.Name)
	}

	err = n.platformService.CreateExternalEndpoint(instance)
	if err != nil {
		return &instance, errors.Wrapf(err, "[ERROR] Failed to create External Route for %v/%v", instance.Namespace, instance.Name)
	}

	return &instance, nil
}
