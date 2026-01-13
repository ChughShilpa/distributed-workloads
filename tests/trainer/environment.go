/*
Copyright 2025.

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

package trainer

import (
	"os"
)

const (
	// The environment variable referring to image simulating sleep condition in container
	sleepImageEnvVar = "SLEEP_IMAGE"
)

func GetSleepImage() string {
	return lookupEnvOrDefault(sleepImageEnvVar, "gcr.io/k8s-staging-perf-tests/sleep@sha256:8d91ddf9f145b66475efda1a1b52269be542292891b5de2a7fad944052bab6ea")
}

func lookupEnvOrDefault(key, value string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return value
}

