package ldclient

import (
	"time"
)

// A User contains specific attributes of a user browsing your site. The only mandatory property property is the Key,
// which must uniquely identify each user. For authenticated users, this may be a username or e-mail address. For anonymous users,
// this could be an IP address or session ID.
//
// Besides the mandatory Key, User supports two kinds of optional attributes: interpreted attributes (e.g. Ip and Country)
// and custom attributes.  LaunchDarkly can parse interpreted attributes and attach meaning to them. For example, from an Ip address, LaunchDarkly can
// do a geo IP lookup and determine the user's country.
//
// Custom attributes are not parsed by LaunchDarkly. They can be used in custom rules-- for example, a custom attribute such as "customer_ranking" can be used to
// launch a feature to the top 10% of users on a site.
type User struct {
	Key       *string                      `json:"key,omitempty" bson:"key,omitempty"`
	Secondary *string                      `json:"secondary,omitempty" bson:"secondary,omitempty"`
	Ip        *string                      `json:"ip,omitempty" bson:"ip,omitempty"`
	Country   *string                      `json:"country,omitempty" bson:"country,omitempty"`
	Email     *string                      `json:"email,omitempty" bson:"email,omitempty"`
	FirstName *string                      `json:"firstName,omitempty" bson:"firstName,omitempty"`
	LastName  *string                      `json:"lastName,omitempty" bson:"lastName,omitempty"`
	Avatar    *string                      `json:"avatar,omitempty" bson:"avatar,omitempty"`
	Name      *string                      `json:"name,omitempty" bson:"name,omitempty"`
	Anonymous *bool                        `json:"anonymous,omitempty" bson:"anonymous,omitempty"`
	Custom    *map[string]interface{}      `json:"custom,omitempty" bson:"custom,omitempty"`
	Derived   map[string]*DerivedAttribute `json:"derived,omitempty" bson:"derived,omitempty"`

	// PrivateAttributes contains a list of attribute names that were included in the user,
	// but were marked as private. As such, these attributes are not included in the fields above.
	PrivateAttributes []string `json:"privateAttrs,omitempty" bson:"privateAttrs,omitempty"`

	// This contains list of attributes to keep private, whether they appear at the top-level or Custom
	// The attribute "key" is always sent regardless of whether it is in this list, and "custom" cannot be used to
	// eliminate all custom attributes
	PrivateAttributeNames []string `json:"-" bson:"-"`
}

// DerivedAttribute is an entry in a Derived attribute map and is for internal use by LaunchDarkly only. Derived attributes
// sent to LaunchDarkly are ignored.
type DerivedAttribute struct {
	Value       interface{} `json:"value" bson:"value"`
	LastDerived time.Time   `json:"lastDerived" bson:"lastDerived"`
}

// NewUser creates a new user identified by the given key.
func NewUser(key string) User {
	return User{Key: &key}
}

// NewAnonymousUser creates a new anonymous user identified by the given key.
func NewAnonymousUser(key string) User {
	anonymous := true
	return User{Key: &key, Anonymous: &anonymous}
}

func (user User) valueOf(attr string) (interface{}, bool) {
	if attr == "key" {
		if user.Key != nil {
			return *user.Key, false
		}
	} else if attr == "ip" {
		if user.Ip != nil {
			return *user.Ip, false
		}
	} else if attr == "country" {
		if user.Country != nil {
			return *user.Country, false
		}
	} else if attr == "email" {
		if user.Email != nil {
			return *user.Email, false
		}
	} else if attr == "firstName" {
		if user.FirstName != nil {
			return *user.FirstName, false
		}
	} else if attr == "lastName" {
		if user.LastName != nil {
			return *user.LastName, false
		}
	} else if attr == "avatar" {
		if user.Avatar != nil {
			return *user.Avatar, false
		}
	} else if attr == "name" {
		if user.Name != nil {
			return *user.Name, false
		}
	} else if attr == "anonymous" {
		if user.Anonymous != nil {
			return *user.Anonymous, false
		}
	}

	// Select a custom attribute
	if user.Custom == nil {
		return nil, true
	}

	v := (*user.Custom)[attr]

	if v == nil {
		return nil, true
	}

	return v, false
}
