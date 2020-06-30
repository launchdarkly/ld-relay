package lduser

import (
	"encoding/json"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
)

type userForSerialization struct {
	Key                   string                 `json:"key"`
	Secondary             ldvalue.OptionalString `json:"secondary"`
	IP                    ldvalue.OptionalString `json:"ip"`
	Country               ldvalue.OptionalString `json:"country"`
	Email                 ldvalue.OptionalString `json:"email"`
	FirstName             ldvalue.OptionalString `json:"firstName"`
	LastName              ldvalue.OptionalString `json:"lastName"`
	Avatar                ldvalue.OptionalString `json:"avatar"`
	Name                  ldvalue.OptionalString `json:"name"`
	Anonymous             ldvalue.Value          `json:"anonymous"`
	Custom                ldvalue.Value          `json:"custom"`
	PrivateAttributeNames []UserAttribute        `json:"privateAttributeNames"`
}

// String returns a simple string representation of a user.
//
// This currently uses the same JSON string representation as User.MarshalJSON(). Do not rely on this
// specific behavior of String(); it is intended for convenience in debugging.
func (u User) String() string {
	if bytes, err := json.Marshal(u); err == nil {
		return string(bytes)
	}
	return ""
}

// MarshalJSON provides JSON serialization for User when using json.MarshalJSON.
//
// This is LaunchDarkly's standard JSON representation for user properties, in which all of the built-in
// properties are at the top level along with a "custom" property that is an object containing all of
// the custom properties.
//
// It does not produce the most compact representation possible: top-level properties that have not been
// set will have "propertyName":null instead of being entirely omitted, and if there are no custom
// properties there will be an empty "custom":{}.
func (u User) MarshalJSON() ([]byte, error) {
	ufs := userForSerialization{
		Key:       u.key,
		Secondary: u.secondary,
		IP:        u.ip,
		Country:   u.country,
		Email:     u.email,
		FirstName: u.firstName,
		LastName:  u.lastName,
		Avatar:    u.avatar,
		Name:      u.name,
		Anonymous: u.anonymous,
		Custom:    u.custom,
	}
	for a := range u.privateAttributes {
		ufs.PrivateAttributeNames = append(ufs.PrivateAttributeNames, a)
	}
	return json.Marshal(ufs)
}

// UnmarshalJSON provides JSON deserialization for User when using json.UnmarshalJSON.
//
// This is LaunchDarkly's standard JSON representation for user properties, in which all of the built-in
// properties are at the top level along with a "custom" property that is an object containing all of
// the custom properties. Omitted properties are treated as empty.
func (u *User) UnmarshalJSON(data []byte) error {
	var ufs userForSerialization
	if err := json.Unmarshal(data, &ufs); err != nil {
		return err
	}
	*u = User{
		key:       ufs.Key,
		secondary: ufs.Secondary,
		ip:        ufs.IP,
		country:   ufs.Country,
		email:     ufs.Email,
		firstName: ufs.FirstName,
		lastName:  ufs.LastName,
		avatar:    ufs.Avatar,
		name:      ufs.Name,
		anonymous: ufs.Anonymous,
		custom:    ufs.Custom,
	}
	if len(ufs.PrivateAttributeNames) > 0 {
		u.privateAttributes = make(map[UserAttribute]struct{})
		for _, a := range ufs.PrivateAttributeNames {
			u.privateAttributes[a] = struct{}{}
		}
	}
	return nil
}
