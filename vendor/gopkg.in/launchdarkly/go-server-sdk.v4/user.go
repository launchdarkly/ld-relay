package ldclient

import (
	"encoding/json"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v1/ldvalue"
)

// A User contains specific attributes of a user browsing your site. The only mandatory property property is the Key,
// which must uniquely identify each user. For authenticated users, this may be a username or e-mail address. For anonymous users,
// this could be an IP address or session ID.
//
// Besides the mandatory Key, User supports two kinds of optional attributes: interpreted attributes (e.g. Ip and Country)
// and custom attributes.  LaunchDarkly can parse interpreted attributes and attach meaning to them. For example, from an IP address, LaunchDarkly can
// do a geo IP lookup and determine the user's country.
//
// Custom attributes are not parsed by LaunchDarkly. They can be used in custom rules-- for example, a custom attribute such as "customer_ranking" can be used to
// launch a feature to the top 10% of users on a site.
//
// User fields will be made private in the future, accessible only via getter methods, to prevent unsafe
// modification of users after they are created. The preferred method of constructing a User is to use either
// a simple constructor (NewUser, NewAnonymousUser) or the builder pattern with NewUserBuilder. If you do set
// the User fields directly, it is important not to change any map/slice elements, and not change a string
// that is pointed to by an existing pointer, after the User has been passed to any SDK methods; otherwise,
// flag evaluations and analytics events may refer to the wrong user properties (or, in the case of a map, you
// may even cause a concurrent modification panic).
type User struct {
	// Key is the unique key of the user.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	Key *string `json:"key,omitempty" bson:"key,omitempty"`
	// SecondaryKey is the secondary key of the user.
	//
	// This affects feature flag targeting (https://docs.launchdarkly.com/docs/targeting-users#section-targeting-rules-based-on-user-attributes)
	// as follows: if you have chosen to bucket users by a specific attribute, the secondary key (if set)
	// is used to further distinguish between users who are otherwise identical according to that attribute.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	Secondary *string `json:"secondary,omitempty" bson:"secondary,omitempty"`
	// Ip is the IP address attribute of the user.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	Ip *string `json:"ip,omitempty" bson:"ip,omitempty"`
	// Country is the country attribute of the user.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	Country *string `json:"country,omitempty" bson:"country,omitempty"`
	// Email is the email address attribute of the user.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	Email *string `json:"email,omitempty" bson:"email,omitempty"`
	// FirstName is the first name attribute of the user.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	FirstName *string `json:"firstName,omitempty" bson:"firstName,omitempty"`
	// LastName is the last name attribute of the user.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	LastName *string `json:"lastName,omitempty" bson:"lastName,omitempty"`
	// Avatar is the avatar URL attribute of the user.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	Avatar *string `json:"avatar,omitempty" bson:"avatar,omitempty"`
	// Name is the name attribute of the user.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	Name *string `json:"name,omitempty" bson:"name,omitempty"`
	// Anonymous indicates whether the user is anonymous.
	//
	// If a user is anonymous, the user key will not appear on your LaunchDarkly dashboard.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	Anonymous *bool `json:"anonymous,omitempty" bson:"anonymous,omitempty"`
	// Custom is the user's map of custom attribute names and values.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	Custom *map[string]interface{} `json:"custom,omitempty" bson:"custom,omitempty"`
	// Derived is used internally by the SDK.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	Derived map[string]*DerivedAttribute `json:"derived,omitempty" bson:"derived,omitempty"`

	// PrivateAttributes contains a list of attribute names that were included in the user,
	// but were marked as private. As such, these attributes are not included in the fields above.
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	PrivateAttributes []string `json:"privateAttrs,omitempty" bson:"privateAttrs,omitempty"`

	// This contains list of attributes to keep private, whether they appear at the top-level or Custom
	// The attribute "key" is always sent regardless of whether it is in this list, and "custom" cannot be used to
	// eliminate all custom attributes
	//
	// Deprecated: Direct access to User fields is now deprecated in favor of UserBuilder. In a future version,
	// User fields will be private and only accessible via getter methods.
	PrivateAttributeNames []string `json:"-" bson:"-"`
}

// GetKey gets the unique key of the user.
func (u User) GetKey() string {
	// Key is only nullable for historical reasons - all users should have a key
	if u.Key == nil {
		return ""
	}
	return *u.Key
}

// GetSecondaryKey returns the secondary key of the user, if any.
//
// This affects feature flag targeting (https://docs.launchdarkly.com/docs/targeting-users#section-targeting-rules-based-on-user-attributes)
// as follows: if you have chosen to bucket users by a specific attribute, the secondary key (if set)
// is used to further distinguish between users who are otherwise identical according to that attribute.
func (u User) GetSecondaryKey() ldvalue.OptionalString {
	return ldvalue.NewOptionalStringFromPointer(u.Secondary)
}

// GetIP() returns the IP address attribute of the user, if any.
func (u User) GetIP() ldvalue.OptionalString {
	return ldvalue.NewOptionalStringFromPointer(u.Ip)
}

// GetCountry() returns the country attribute of the user, if any.
func (u User) GetCountry() ldvalue.OptionalString {
	return ldvalue.NewOptionalStringFromPointer(u.Country)
}

// GetEmail() returns the email address attribute of the user, if any.
func (u User) GetEmail() ldvalue.OptionalString {
	return ldvalue.NewOptionalStringFromPointer(u.Email)
}

// GetFirstName() returns the first name attribute of the user, if any.
func (u User) GetFirstName() ldvalue.OptionalString {
	return ldvalue.NewOptionalStringFromPointer(u.FirstName)
}

// GetLastName() returns the last name attribute of the user, if any.
func (u User) GetLastName() ldvalue.OptionalString {
	return ldvalue.NewOptionalStringFromPointer(u.LastName)
}

// GetAvatar() returns the avatar URL attribute of the user, if any.
func (u User) GetAvatar() ldvalue.OptionalString {
	return ldvalue.NewOptionalStringFromPointer(u.Avatar)
}

// GetName() returns the full name attribute of the user, if any.
func (u User) GetName() ldvalue.OptionalString {
	return ldvalue.NewOptionalStringFromPointer(u.Name)
}

// GetAnonymous() returns the anonymous attribute of the user.
//
// If a user is anonymous, the user key will not appear on your LaunchDarkly dashboard.
func (u User) GetAnonymous() bool {
	return u.Anonymous != nil && *u.Anonymous
}

// GetAnonymousOptional() returns the anonymous attribute of the user, with a second value indicating
// whether that attribute was defined for the user or not.
func (u User) GetAnonymousOptional() (bool, bool) {
	return u.GetAnonymous(), u.Anonymous != nil
}

// GetCustom() returns a custom attribute of the user by name. The boolean second return value indicates
// whether any value was set for this attribute or not.
//
// The value is returned using the ldvalue.Value type, which can contain any type supported by JSON:
// boolean, number, string, array (slice), or object (map). Use Value methods to access the value as
// the desired type, rather than casting it. If the attribute did not exist, the value will be
// ldvalue.Null() and the second return value will be false.
func (u User) GetCustom(attrName string) (ldvalue.Value, bool) {
	if u.Custom == nil {
		return ldvalue.Null(), false
	}
	value, found := (*u.Custom)[attrName]
	// Note: since the value is currently represented internally as interface{}, we are using a
	// method that wraps the same interface{} in a Value, to avoid the overhead of a deep copy.
	// This is designated as Unsafe because it is possible (using another Unsafe method) to access
	// the interface{} value directly and, if it contains a slice or map, modify it. In a future
	// version when the User fields are no longer exposed and backward compatibility is no longer
	// necessary, a custom attribute will be stored as a completely immutable Value.
	return ldvalue.UnsafeUseArbitraryValue(value), found //nolint // allow deprecated usage
}

// GetCustomKeys() returns the keys of all custom attributes that have been set on this user.
func (u User) GetCustomKeys() []string {
	if u.Custom == nil || len(*u.Custom) == 0 {
		return nil
	}
	keys := make([]string, 0, len(*u.Custom))
	for key := range *u.Custom {
		keys = append(keys, key)
	}
	return keys
}

// Equal tests whether two users have equal attributes.
//
// Regular struct equality comparison is not allowed for User because it can contain slices and
// maps. This method is faster than using reflect.DeepEqual(), and also correctly ignores
// insignificant differences in the internal representation of the attributes.
func (u User) Equal(other User) bool {
	if u.GetKey() != other.GetKey() ||
		u.GetSecondaryKey() != other.GetSecondaryKey() ||
		u.GetIP() != other.GetIP() ||
		u.GetCountry() != other.GetCountry() ||
		u.GetEmail() != other.GetEmail() ||
		u.GetFirstName() != other.GetFirstName() ||
		u.GetLastName() != other.GetLastName() ||
		u.GetAvatar() != other.GetAvatar() ||
		u.GetName() != other.GetName() ||
		u.GetAnonymous() != other.GetAnonymous() {
		return false
	}
	if (u.Anonymous == nil) != (other.Anonymous == nil) ||
		u.Anonymous != nil && *u.Anonymous != *other.Anonymous {
		return false
	}
	if (u.Custom == nil) != (other.Custom == nil) ||
		u.Custom != nil && len(*u.Custom) != len(*other.Custom) {
		return false
	}
	if u.Custom != nil {
		for k, v := range *u.Custom {
			v1, ok := (*other.Custom)[k]
			if !ok || v != v1 {
				return false
			}
		}
	}
	if !stringSlicesEqual(u.PrivateAttributeNames, other.PrivateAttributeNames) {
		return false
	}
	if !stringSlicesEqual(u.PrivateAttributes, other.PrivateAttributes) {
		return false
	}
	return true
}

// String returns a simple string representation of a user.
func (u User) String() string {
	bytes, _ := json.Marshal(u)
	return string(bytes)
}

// Used internally in evaluations. The second return value is true if the attribute exists for this user,
// false if not.
func (u User) valueOf(attr string) (interface{}, bool) {
	if attr == "key" {
		if u.Key != nil {
			return *u.Key, true
		}
		return nil, false
	} else if attr == "ip" {
		return optionalStringAsEmptyInterface(u.GetIP())
	} else if attr == "country" {
		return optionalStringAsEmptyInterface(u.GetCountry())
	} else if attr == "email" {
		return optionalStringAsEmptyInterface(u.GetEmail())
	} else if attr == "firstName" {
		return optionalStringAsEmptyInterface(u.GetFirstName())
	} else if attr == "lastName" {
		return optionalStringAsEmptyInterface(u.GetLastName())
	} else if attr == "avatar" {
		return optionalStringAsEmptyInterface(u.GetAvatar())
	} else if attr == "name" {
		return optionalStringAsEmptyInterface(u.GetName())
	} else if attr == "anonymous" {
		value, ok := u.GetAnonymousOptional()
		return value, ok
	}

	// Select a custom attribute
	value, ok := u.GetCustom(attr)
	// Currently our evaluation logic still uses interface{} rather than Value; we can use the faster
	// Unsafe method to avoid a deep copy in this context
	return value.UnsafeArbitraryValue(), ok //nolint // allow deprecated usage
}

func optionalStringAsEmptyInterface(os ldvalue.OptionalString) (interface{}, bool) {
	if os.IsDefined() {
		return os.StringValue(), true
	}
	return nil, false
}

// DerivedAttribute is an entry in a Derived attribute map and is for internal use by LaunchDarkly only. Derived attributes
// sent to LaunchDarkly are ignored.
//
// Deprecated: this type is for internal use and will be removed in a future version.
type DerivedAttribute struct {
	Value       interface{} `json:"value" bson:"value"`
	LastDerived time.Time   `json:"lastDerived" bson:"lastDerived"`
}
