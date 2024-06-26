/*
Copyright 2024 The etcd-operator Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"context"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"

	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:golint,revive
)

// Run executes the provided command within this context
func Run(cmd *exec.Cmd) ([]byte, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir

	if err := os.Chdir(cmd.Dir); err != nil {
		_, err := fmt.Fprintf(GinkgoWriter, "chdir dir: %s\n", err)
		if err != nil {
			return nil, err
		}
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	_, err := fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	if err != nil {
		return nil, err
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}

	return output, nil
}

// GetNonEmptyLines converts given command output string into individual objects
// according to line breakers, and ignores the empty elements in it.
func GetNonEmptyLines(output string) []string {
	var res []string
	elements := strings.Split(output, "\n")
	for _, element := range elements {
		if element != "" {
			res = append(res, element)
		}
	}

	return res
}

// GetProjectDir will return the directory where the project is
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, err
	}
	wd = strings.Replace(wd, "/test/e2e", "", -1)
	return wd, nil
}

// GetFreePort asks the kernel for a free open port that is ready to use.
func GetFreePort() (port int, err error) {
	var a *net.TCPAddr
	if a, err = net.ResolveTCPAddr("tcp", "localhost:0"); err == nil {
		var l *net.TCPListener
		if l, err = net.ListenTCP("tcp", a); err == nil {
			defer func(l *net.TCPListener) {
				err := l.Close()
				if err != nil {
					log.Fatal(err)
				}
			}(l)
			return l.Addr().(*net.TCPAddr).Port, nil
		}
	}
	return
}

// GetEtcdClient creates client for interacting with etcd.
func GetEtcdClient(endpoints []string) *clientv3.Client {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	return cli
}

// IsEtcdClusterHealthy checks etcd cluster health.
func IsEtcdClusterHealthy(endpoints []string) bool {
	// Should be changed when etcd is healthy
	health := false

	// Configure client
	client := GetEtcdClient(endpoints)
	defer func(client *clientv3.Client) {
		err := client.Close()
		if err != nil {
			log.Fatal(err)
		}
	}(client)

	// Prepare the maintenance client
	maint := clientv3.NewMaintenance(client)

	// Context for the call
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Perform the status call to check health
	for i := range endpoints {
		resp, err := maint.Status(ctx, endpoints[i])
		if err != nil {
			log.Fatalf("Failed to get endpoint health: %v", err)
		} else {
			if resp.Errors == nil {
				fmt.Printf("Endpoint is healthy: %s\n", resp.Version)
				health = true
			}
		}
	}
	return health
}
