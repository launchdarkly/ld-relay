package ldclient

import (
	"encoding/json"

	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"
)

type serializableUser struct {
	User
	filter *userFilter
}

type userFilter struct {
	allAttributesPrivate    bool
	globalPrivateAttributes []string
	loggers                 ldlog.Loggers
	logUserKeyInErrors      bool
}

func newUserFilter(config Config) userFilter {
	return userFilter{
		allAttributesPrivate:    config.AllAttributesPrivate,
		globalPrivateAttributes: config.PrivateAttributeNames,
		loggers:                 config.Loggers,
		logUserKeyInErrors:      config.LogUserKeyInErrors,
	}
}

const userSerializationErrorMessage = "An error occurred while processing custom attributes for %s. If this is a concurrent" +
	" modification error, check that you are not modifying custom attributes in a User after you have evaluated a flag with that User. The" +
	" custom attributes for this user have been dropped from analytics data. Error: %s"

// Returns a version of the user data that is suitable for JSON serialization in event data.
// If neither the configuration nor the user specifies any private attributes, then this is the same
// as the original user. Otherwise, it is a copy which may have some attributes removed (with the
// PrivateAttributes property set to a list of their names).
//
// This function, and the custom marshaller for serializableUser, also guard against a potential
// concurrent modification error on the user's custom attributes map. We can't prevent someone in
// another goroutine from modifying that map and causing an error when we iterate over it (either
// here, or during JSON serialization), but we can at least recover from such an error and log the
// problem. In that case, all of the custom attributes for the user will be lost (since we have no
// way to know whether they are still correct after the concurrent modification).
func (uf *userFilter) scrubUser(user User) (ret *serializableUser) {
	ret = &serializableUser{User: user, filter: uf}
	if len(user.PrivateAttributeNames) == 0 && len(uf.globalPrivateAttributes) == 0 && !uf.allAttributesPrivate {
		return
	}

	isPrivate := map[string]bool{}
	for _, n := range uf.globalPrivateAttributes {
		isPrivate[n] = true
	}
	for _, n := range user.PrivateAttributeNames {
		isPrivate[n] = true
	}
	ret.User.PrivateAttributeNames = nil // this property is not used in the output schema for events
	ret.User.PrivateAttributes = nil     // see below
	// Because we're only resetting these properties if we're going to proceed with the scrubbing logic, it is
	// possible to pass an already-scrubbed user to this function (with, potentially, some attribute names in
	// PrivateAttributes) and get back the same object, as long as the configuration does not have private
	// attributes enabled. This allows us to reuse the event processor code in ld-relay, where we may have to
	// reprocess events that have already been through the scrubbing process.

	if !isEmpty(user.Avatar) && (uf.allAttributesPrivate || isPrivate["avatar"]) {
		ret.User.Avatar = nil
		ret.User.PrivateAttributes = append(ret.User.PrivateAttributes, "avatar")
	}

	if !isEmpty(user.Country) && (uf.allAttributesPrivate || isPrivate["country"]) {
		ret.User.Country = nil
		ret.User.PrivateAttributes = append(ret.User.PrivateAttributes, "country")
	}

	if !isEmpty(user.Ip) && (uf.allAttributesPrivate || isPrivate["ip"]) {
		ret.User.Ip = nil
		ret.User.PrivateAttributes = append(ret.User.PrivateAttributes, "ip")
	}

	if !isEmpty(user.FirstName) && (uf.allAttributesPrivate || isPrivate["firstName"]) {
		ret.User.FirstName = nil
		ret.User.PrivateAttributes = append(ret.User.PrivateAttributes, "firstName")
	}

	if !isEmpty(user.LastName) && (uf.allAttributesPrivate || isPrivate["lastName"]) {
		ret.User.LastName = nil
		ret.User.PrivateAttributes = append(ret.User.PrivateAttributes, "lastName")
	}

	if !isEmpty(user.Name) && (uf.allAttributesPrivate || isPrivate["name"]) {
		ret.User.Name = nil
		ret.User.PrivateAttributes = append(ret.User.PrivateAttributes, "name")
	}

	if !isEmpty(user.Secondary) && (uf.allAttributesPrivate || isPrivate["secondary"]) {
		ret.User.Secondary = nil
		ret.User.PrivateAttributes = append(ret.User.PrivateAttributes, "secondary")
	}

	if !isEmpty(user.Email) && (uf.allAttributesPrivate || isPrivate["email"]) {
		ret.User.Email = nil
		ret.User.PrivateAttributes = append(ret.User.PrivateAttributes, "email")
	}

	if user.Custom != nil {
		// Any panics that happen from this point on (presumably due to concurrent modification of the
		// custom attributes map) will be caught here, in which case we simply drop the custom attributes.
		defer func() {
			if r := recover(); r != nil {
				uf.loggers.Errorf(userSerializationErrorMessage, describeUserForErrorLog(&user, uf.logUserKeyInErrors), r)
				ret.User.Custom = nil
			}
		}()
		var custom = map[string]interface{}{}
		for k, v := range *user.Custom {
			if uf.allAttributesPrivate || isPrivate[k] {
				ret.User.PrivateAttributes = append(ret.User.PrivateAttributes, k)
			} else {
				custom[k] = v
			}
		}
		if len(custom) > 0 {
			ret.User.Custom = &custom
		} else {
			ret.User.Custom = nil
		}
	}

	return ret
}

func (u serializableUser) MarshalJSON() (output []byte, err error) {
	marshalUserWithoutCustomAttrs := func(err interface{}) ([]byte, error) {
		if me, ok := err.(*json.MarshalerError); ok {
			err = me.Err
		}
		u.filter.loggers.Errorf(userSerializationErrorMessage, describeUserForErrorLog(&u.User, u.filter.logUserKeyInErrors), err)
		u.User.Custom = nil
		return json.Marshal(u.User)
	}
	defer func() {
		// See comments on scrubUser.
		if r := recover(); r != nil {
			output, err = marshalUserWithoutCustomAttrs(r)
		}
	}()
	// Note that in some versions of Go, any panic within json.Marshal is automatically caught and converted into
	// an error result. Since there shouldn't be any way for serialization to fail on any user attributes other
	// than the custom ones, we want to treat that the same as a panic.
	output, err = json.Marshal(u.User)
	if err != nil {
		output, err = marshalUserWithoutCustomAttrs(err)
	}
	return
}

func isEmpty(s *string) bool {
	return s == nil || *s == ""
}
