/*
Foliage Web Services

Foliage web services, owner: DevProd Services & Integrations team

API version: 1.0.0
*/

// Code generated by OpenAPI Generator (https://openapi-generator.tech); DO NOT EDIT.

package fws

import (
	"encoding/json"
	"fmt"
)


// GetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue struct for GetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue
type GetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue struct {
	TeamData *TeamData
}

// Unmarshal JSON data into any of the pointers in the struct
func (dst *GetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue) UnmarshalJSON(data []byte) error {
	var err error
	// this object is nullable so check if the payload is null or empty string
	if string(data) == "" || string(data) == "{}" {
		return nil
	}

	// try to unmarshal JSON data into TeamData
	err = json.Unmarshal(data, &dst.TeamData);
	if err == nil {
		jsonTeamData, _ := json.Marshal(dst.TeamData)
		if string(jsonTeamData) == "{}" { // empty struct
			dst.TeamData = nil
		} else {
			return nil // data stored in dst.TeamData, return on the first match
		}
	} else {
		dst.TeamData = nil
	}

	return fmt.Errorf("data failed to match schemas in anyOf(GetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue)")
}

// Marshal data from the first non-nil pointers in the struct to JSON
func (src GetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue) MarshalJSON() ([]byte, error) {
	if src.TeamData != nil {
		return json.Marshal(&src.TeamData)
	}

	return nil, nil // no data in anyOf schemas
}


type NullableGetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue struct {
	value *GetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue
	isSet bool
}

func (v NullableGetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue) Get() *GetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue {
	return v.value
}

func (v *NullableGetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue) Set(val *GetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue) {
	v.value = val
	v.isSet = true
}

func (v NullableGetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue) IsSet() bool {
	return v.isSet
}

func (v *NullableGetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue) Unset() {
	v.value = nil
	v.isSet = false
}

func NewNullableGetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue(val *GetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue) *NullableGetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue {
	return &NullableGetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue{value: val, isSet: true}
}

func (v NullableGetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.value)
}

func (v *NullableGetRegexMappingApiOwnerRegexByProjectProjectIdGet200ResponseValue) UnmarshalJSON(src []byte) error {
	v.isSet = true
	return json.Unmarshal(src, &v.value)
}


