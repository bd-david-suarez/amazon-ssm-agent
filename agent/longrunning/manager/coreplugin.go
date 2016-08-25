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

// Package manager encapsulates everything related to long running plugin manager that starts, stops & configures long running plugins
package manager

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/aws/amazon-ssm-agent/agent/context"
	"github.com/aws/amazon-ssm-agent/agent/contracts"
	managerContracts "github.com/aws/amazon-ssm-agent/agent/longrunning/plugin"
	"github.com/aws/amazon-ssm-agent/agent/task"
	"github.com/aws/amazon-ssm-agent/agent/times"
	"github.com/carlescere/scheduler"
)

const (
	//name is the core plugin name for long running plugins manager
	Name = "LongRunningPluginsManager"

	//number of long running workers
	NumberOfLongRunningPluginWorkers = 5

	//number of cancel workers
	NumberOfCancelWorkers = 5

	//poll frequency for managing lifecycle of long running plugins
	PollFrequencyMinutes = 15

	//hardStopTimeout is the time before the manager will be shutdown during a hardstop = 4 seconds
	HardStopTimeout = 4 * time.Second

	//softStopTimeout is the time before the manager will be shutdown during a softstop = 20 seconds
	SoftStopTimeout = 20 * time.Second
)

// Manager is the core plugin - that manages long running plugins
type Manager struct {
	context context.T

	//task pool to run long running plugins
	startPlugin task.Pool

	//task pool to stop long running plugins
	stopPlugin task.Pool

	//stores all writeable information about currently long running plugins
	runningPlugins map[string]managerContracts.PluginInfo

	//stores references of all the registered long running plugins
	registeredPlugins map[string]managerContracts.Plugin

	//manages lifecycle of all long running plugins
	managingLifeCycleJob *scheduler.Job
}

var singletonInstance *Manager
var once sync.Once

// EnsureManagerIsInitialized ensures that manager is initialized at least once
func EnsureInitialization(context context.T) {
	//todo: After we start using 1 task pool for entire agent (even for core plugins), we can then move all initializations to init()

	//only components with access to context are expected to call this

	//this ensures that only one instance of lrpm exists
	once.Do(func() {
		managerContext := context.With("[" + Name + "]")
		log := managerContext.Log()
		//initialize pluginsInfo (which will store all information about long running plugins)
		plugins := make(map[string]managerContracts.PluginInfo)
		//load all registered plugins
		regPlugins := RegisteredPlugins()
		jsonB, _ := json.Marshal(&regPlugins)
		log.Infof("registered plugins: %s", string(jsonB))

		// startPlugin and stopPlugin will be processed by separate worker pools
		// so we can define the number of workers for each pool
		cancelWaitDuration := 10000 * time.Millisecond
		clock := times.DefaultClock
		startPluginPool := task.NewPool(log, NumberOfLongRunningPluginWorkers, cancelWaitDuration, clock)
		stopPluginPool := task.NewPool(log, NumberOfCancelWorkers, cancelWaitDuration, clock)

		singletonInstance = &Manager{
			context:           managerContext,
			startPlugin:       startPluginPool,
			stopPlugin:        stopPluginPool,
			runningPlugins:    plugins,
			registeredPlugins: regPlugins,
		}
	})

}

// GetInstance returns an instance of Manager if its initialized otherwise it returns an error
func GetInstance() (*Manager, error) {
	lock.Lock()
	defer lock.Unlock()

	if singletonInstance == nil {
		return nil, errors.New("lrpm isn't initialized yet")
	} else {
		return singletonInstance, nil
	}
}

// GetRegisteredPlugins returns a map of all registered long running plugins
func (m *Manager) GetRegisteredPlugins() map[string]managerContracts.Plugin {
	return m.registeredPlugins
}

// Name returns the Plugin Name
func (m *Manager) Name() string {
	return Name
}

// Execute starts long running plugin manager
func (m *Manager) Execute(context context.T) (err error) {

	log := m.context.Log()
	log.Infof("starting long running plugin manager")

	//read from data store to determine if there were any previously long running plugins which need to be started again
	m.runningPlugins, err = dataStore.Read()
	if err != nil {
		log.Errorf("%s is exiting - unable to read from data store", m.Name())
		return
	}

	//revive older long running plugins if they were running before
	if len(m.runningPlugins) > 0 {
		var p managerContracts.Plugin
		for pluginName, _ := range m.runningPlugins {
			//get the corresponding registered plugin
			p = m.registeredPlugins[pluginName]
			log.Infof("Detected %s as a previously executing long running plugin. Starting that plugin again", p.Info.Name)

			//submit the work of long running plugin to the task pool
			/*
				Note: All long running plugins are singleton in nature - hence jobId = plugin name.
				This is in sync with our task-pool - which rejects jobs with duplicate jobIds.
			*/
			//todo: implement the singleton thing - ensure that there are no more than 1 cloudwatch plugin running at a time
			//todo: orchestrationDir should be set accordingly - 3rd parameter for Start
			p.Handler.Start(m.context, p.Info.Configuration, "", task.NewChanneledCancelFlag())
		}
	} else {
		log.Infof("there aren't any long running plugin to execute")
	}

	//schedule periodic health check of all long running plugins
	if m.managingLifeCycleJob, err = scheduler.Every(PollFrequencyMinutes).Minutes().Run(m.ensurePluginsAreRunning); err != nil {
		context.Log().Errorf("unable to schedule long running plugins manager. %v", err)
	}

	return
}

// RequestStop handles the termination of the message processor plugin job
func (m *Manager) RequestStop(stopType contracts.StopType) (err error) {
	var waitTimeout time.Duration

	if stopType == contracts.StopTypeSoftStop {
		waitTimeout = SoftStopTimeout
	} else {
		waitTimeout = HardStopTimeout
	}

	var wg sync.WaitGroup

	// stop lifecycle management job that monitors execution of all long running plugins
	m.stopLifeCycleManagementJob()

	//there is no need to stop all individual plugins - because when the task pools are shutdown - all corresponding
	//jobs are also shutdown accordingly.

	// shutdown the send command pool in a separate go routine
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.startPlugin.ShutdownAndWait(waitTimeout)
	}()

	// shutdown the cancel command pool in a separate go routine
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.stopPlugin.ShutdownAndWait(waitTimeout)
	}()

	// wait for everything to shutdown
	wg.Wait()
	return nil
}
