// Copyright 2016 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the
// License is located at
//
// http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing
// permissions and limitations under the License.

// Package plugin contains general interfaces and types relevant to plugins.
// It also provides the methods for registering plugins.
package plugin

import (
	"sync"

	"github.com/aws/amazon-ssm-agent/agent/context"
	"github.com/aws/amazon-ssm-agent/agent/contracts"
	"github.com/aws/amazon-ssm-agent/agent/plugins/lrpminvoker"
	"github.com/aws/amazon-ssm-agent/agent/plugins/pluginutil"
	"github.com/aws/amazon-ssm-agent/agent/plugins/runcommand"
	"github.com/aws/amazon-ssm-agent/agent/plugins/updatessmagent"
	"github.com/aws/amazon-ssm-agent/agent/task"
)

// T is the interface type for plugins.
type T interface {
	Execute(context context.T, config contracts.Configuration, cancelFlag task.CancelFlag) contracts.PluginResult
}

// PluginRegistry stores a set of plugins (both worker and long running plugins), indexed by ID.
type PluginRegistry map[string]T

// registeredExecuters stores the registered plugins.
var registeredExecuters, registeredLongRunningPlugins *PluginRegistry

// RegisteredWorkerPlugins returns all registered core plugins.
func RegisteredWorkerPlugins(context context.T) PluginRegistry {
	if !isLoaded() {
		cache(loadWorkerPlugins(context), loadLongRunningPlugins(context))
	}
	return getCachedWorkerPlugins()
}

// LongRunningPlugins returns a map of long running plugins and their respective handlers
func RegisteredLongRunningPlugins(context context.T) PluginRegistry {
	if !isLoaded() {
		cache(loadWorkerPlugins(context), loadLongRunningPlugins(context))
	}
	return getCachedLongRunningPlugins()
}

var lock sync.RWMutex

func isLoaded() bool {
	lock.RLock()
	defer lock.RUnlock()
	return registeredExecuters != nil
}

func cache(workerPlugins, longRunningPlugins PluginRegistry) {
	lock.Lock()
	defer lock.Unlock()
	registeredExecuters = &workerPlugins
	registeredLongRunningPlugins = &longRunningPlugins
}

func getCachedWorkerPlugins() PluginRegistry {
	lock.RLock()
	defer lock.RUnlock()
	return *registeredExecuters
}

func getCachedLongRunningPlugins() PluginRegistry {
	lock.RLock()
	defer lock.RUnlock()
	return *registeredLongRunningPlugins
}

// loadLongRunningPlugins loads all long running plugins
func loadLongRunningPlugins(context context.T) PluginRegistry {
	log := context.Log()
	var longRunningPlugins = PluginRegistry{}

	//Long running plugins are handled by lrpm. lrpminvoker is a worker plugin that can communicate with lrpm.
	//that's why all long running plugins are first handled by lrpminvoker - which then hands off the work to lrpm.

	if handler, err := lrpminvoker.NewPlugin(pluginutil.DefaultPluginConfig()); err != nil {
		log.Errorf("Failed to load lrpminvoker that will handle all long running plugins - %v", err)
	} else {
		//NOTE: register all long running plugins here

		//registering handler for aws:cloudWatch plugin
		cloudwatchPluginName := "aws:cloudWatch"
		longRunningPlugins[cloudwatchPluginName] = handler
	}

	return longRunningPlugins
}

// loadWorkerPlugins loads all plugins
func loadWorkerPlugins(context context.T) PluginRegistry {
	var workerPlugins = PluginRegistry{}

	for key, value := range loadPlatformIndependentPlugins(context) {
		workerPlugins[key] = value
	}

	for key, value := range loadPlatformDependentPlugins(context) {
		workerPlugins[key] = value
	}

	return workerPlugins
}

// loadPlatformIndependentPlugins registers plugins common to all platforms
func loadPlatformIndependentPlugins(context context.T) PluginRegistry {
	log := context.Log()
	var workerPlugins = PluginRegistry{}

	// registering aws:runPowerShellScript & aws:runShellScript plugin
	runcommandPluginName := runcommand.Name()
	runcommandPlugin, err := runcommand.NewPlugin(pluginutil.DefaultPluginConfig())
	if err != nil {
		log.Errorf("failed to create plugin %s %v", runcommandPluginName, err)
	} else {
		workerPlugins[runcommandPluginName] = runcommandPlugin
	}

	// registering aws:updateSsmAgent plugin
	updateAgentPluginName := updatessmagent.Name()
	updateAgentPlugin, err := updatessmagent.NewPlugin(updatessmagent.GetUpdatePluginConfig(context))
	if err != nil {
		log.Errorf("failed to create plugin %s %v", updateAgentPluginName, err)
	} else {
		workerPlugins[updateAgentPluginName] = updateAgentPlugin
	}

	return workerPlugins
}
