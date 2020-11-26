/*******************************************************************************
*
* Copyright 2020 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

// Package secrets provides convenience functions for working with auth
// credentials.
package secrets

import (
	"fmt"
	"os"
)

//AuthPassword holds either a plain text password or a key for the environment
//variable that has the password as its value.
//The key has the format: `{ fromEnv: ENVIRONMENT_VARIABLE }`.
//
//If a key is given then the password is retrieved from that env variable.
type AuthPassword string

//UnmarshalYAML implements the yaml.Unmarshaler interface.
func (p *AuthPassword) UnmarshalYAML(unmarshal func(interface{}) error) error {
	//plain text password
	var plainTextInput string
	err := unmarshal(&plainTextInput)
	if err == nil {
		*p = AuthPassword(plainTextInput)
		return nil
	}

	//retrieve password from the given environment variable key
	var envVariableInput struct {
		Key string `yaml:"fromEnv"`
	}
	err = unmarshal(&envVariableInput)
	if err != nil {
		return err
	}

	passFromEnv := os.Getenv(envVariableInput.Key)
	if passFromEnv == "" {
		return fmt.Errorf(`environment variable %q is not set`, envVariableInput.Key)
	}

	*p = AuthPassword(passFromEnv)

	return nil
}
