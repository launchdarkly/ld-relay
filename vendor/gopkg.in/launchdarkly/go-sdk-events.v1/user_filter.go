package ldevents

import (
	"encoding/json"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
)

type filteredUser struct {
	Key          string         `json:"key"`
	Secondary    *string        `json:"secondary,omitempty"`
	IP           *string        `json:"ip,omitempty"`
	Country      *string        `json:"country,omitempty"`
	Email        *string        `json:"email,omitempty"`
	FirstName    *string        `json:"firstName,omitempty"`
	LastName     *string        `json:"lastName,omitempty"`
	Avatar       *string        `json:"avatar,omitempty"`
	Name         *string        `json:"name,omitempty"`
	Anonymous    *bool          `json:"anonymous,omitempty"`
	Custom       *ldvalue.Value `json:"custom,omitempty"`
	PrivateAttrs []string       `json:"privateAttrs,omitempty"`
}

type serializableUser struct {
	filteredUser filteredUser
	filter       *userFilter
}

type userFilter struct {
	allAttributesPrivate    bool
	globalPrivateAttributes []lduser.UserAttribute
	loggers                 ldlog.Loggers
	logUserKeyInErrors      bool
}

func newUserFilter(config EventsConfiguration) userFilter {
	return userFilter{
		allAttributesPrivate:    config.AllAttributesPrivate,
		globalPrivateAttributes: config.PrivateAttributeNames,
		loggers:                 config.Loggers,
		logUserKeyInErrors:      config.LogUserKeyInErrors,
	}
}

const userSerializationErrorMessage = "An error occurred while processing custom attributes for %s. If this" +
	" is a concurrent modification error, check that you are not modifying custom attributes in a User after" +
	" you have evaluated a flag with that User. The custom attributes for this user have been dropped from" +
	" analytics data. Error: %s"

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
func (uf *userFilter) scrubUser(user EventUser) (ret *serializableUser) {
	ret = &serializableUser{}
	ret.filter = uf

	ret.filteredUser.Key = user.GetKey()
	if anon, hasAnon := user.GetAnonymousOptional(); hasAnon {
		ret.filteredUser.Anonymous = &anon
	}

	alreadyFiltered := user.AlreadyFilteredAttributes != nil
	// If alreadyFiltered is true, it means this is user data that has already gone through the
	// attribute filtering logic, so the private attribute values have already been removed and their
	// names are in user.AlreadyFilteredAttributes. This happens when Relay receives event data from
	// the PHP SDK. In this case, we do not need to repeat the filtering logic and we do not support
	// re-filtering with a different private attribute configuration.

	if alreadyFiltered ||
		(!user.HasPrivateAttributes() && len(uf.globalPrivateAttributes) == 0 && !uf.allAttributesPrivate) {
		// No need to filter the user attributes
		ret.filteredUser.Secondary = user.GetSecondaryKey().AsPointer()
		ret.filteredUser.IP = user.GetIP().AsPointer()
		ret.filteredUser.Country = user.GetCountry().AsPointer()
		ret.filteredUser.Email = user.GetEmail().AsPointer()
		ret.filteredUser.FirstName = user.GetFirstName().AsPointer()
		ret.filteredUser.LastName = user.GetLastName().AsPointer()
		ret.filteredUser.Avatar = user.GetAvatar().AsPointer()
		ret.filteredUser.Name = user.GetName().AsPointer()
		ret.filteredUser.Custom = user.GetAllCustom().AsPointer()
		if alreadyFiltered {
			ret.filteredUser.PrivateAttrs = user.AlreadyFilteredAttributes
		}
		return
	}

	privateAttrs := []string{}
	isPrivate := func(attrName lduser.UserAttribute) bool {
		if uf.allAttributesPrivate || user.IsPrivateAttribute(attrName) {
			return true
		}
		for _, a := range uf.globalPrivateAttributes {
			if a == attrName {
				return true
			}
		}
		return false
	}
	maybeFilter := func(attr lduser.UserAttribute, getter func(lduser.User) ldvalue.OptionalString) *string {
		value := getter(user.User)
		if value.IsDefined() {
			if isPrivate(attr) {
				privateAttrs = append(privateAttrs, string(attr))
				return nil
			}
			return value.AsPointer()
		}
		return nil
	}
	ret.filteredUser.Secondary = maybeFilter(lduser.SecondaryKeyAttribute, lduser.User.GetSecondaryKey)
	ret.filteredUser.IP = maybeFilter(lduser.IPAttribute, lduser.User.GetIP)
	ret.filteredUser.Country = maybeFilter(lduser.CountryAttribute, lduser.User.GetCountry)
	ret.filteredUser.Email = maybeFilter(lduser.EmailAttribute, lduser.User.GetEmail)
	ret.filteredUser.FirstName = maybeFilter(lduser.FirstNameAttribute, lduser.User.GetFirstName)
	ret.filteredUser.LastName = maybeFilter(lduser.LastNameAttribute, lduser.User.GetLastName)
	ret.filteredUser.Avatar = maybeFilter(lduser.AvatarAttribute, lduser.User.GetAvatar)
	ret.filteredUser.Name = maybeFilter(lduser.NameAttribute, lduser.User.GetName)

	if !user.GetAllCustom().IsNull() {
		// Any panics that happen from this point on (presumably due to concurrent modification of the
		// custom attributes map) will be caught here, in which case we simply drop the custom attributes.
		// Such a concurrent modification shouldn't be possible since the map is not exposed outside of
		// the ldvalue package, but better safe than sorry.
		defer func() {
			if r := recover(); r != nil {
				uf.loggers.Errorf(userSerializationErrorMessage, describeUserForErrorLog(user.GetKey(), uf.logUserKeyInErrors), r)
				ret.filteredUser.Custom = nil
			}
		}()
		filteredCustom := user.GetAllCustom().Transform(func(i int, key string, v ldvalue.Value) (ldvalue.Value, bool) {
			if isPrivate(lduser.UserAttribute(key)) {
				privateAttrs = append(privateAttrs, key)
				return ldvalue.Null(), false
			}
			return v, true
		})
		if filteredCustom.Count() > 0 {
			ret.filteredUser.Custom = &filteredCustom
		}
	}

	ret.filteredUser.PrivateAttrs = privateAttrs
	return //nolint:nakedret // linter complains about "naked return" in a lengthy function
}

func (u serializableUser) MarshalJSON() (output []byte, err error) {
	marshalUserWithoutCustomAttrs := func(err interface{}) ([]byte, error) {
		if me, ok := err.(*json.MarshalerError); ok {
			err = me.Err
		}
		u.filter.loggers.Errorf(
			userSerializationErrorMessage,
			describeUserForErrorLog(u.filteredUser.Key, u.filter.logUserKeyInErrors),
			err,
		)
		u.filteredUser.Custom = nil
		return json.Marshal(u.filteredUser)
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
	output, err = json.Marshal(u.filteredUser)
	if err != nil {
		output, err = marshalUserWithoutCustomAttrs(err)
	}
	return
}
