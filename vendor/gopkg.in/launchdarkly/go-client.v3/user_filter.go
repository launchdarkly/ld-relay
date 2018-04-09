package ldclient

type userFilter struct {
	allAttributesPrivate    bool
	globalPrivateAttributes []string
}

func newUserFilter(config Config) userFilter {
	return userFilter{
		allAttributesPrivate:    config.AllAttributesPrivate,
		globalPrivateAttributes: config.PrivateAttributeNames,
	}
}

func (uf userFilter) scrubUser(user User) *User {
	if len(user.PrivateAttributeNames) == 0 && len(uf.globalPrivateAttributes) == 0 && !uf.allAttributesPrivate {
		return &user
	}

	isPrivate := map[string]bool{}
	for _, n := range uf.globalPrivateAttributes {
		isPrivate[n] = true
	}
	for _, n := range user.PrivateAttributeNames {
		isPrivate[n] = true
	}
	user.PrivateAttributeNames = nil // this property is not used in the output schema for events
	user.PrivateAttributes = nil     // see below
	// Because we're only resetting these properties if we're going to proceed with the scrubbing logic, it is
	// possible to pass an already-scrubbed user to this function (with, potentially, some attribute names in
	// PrivateAttributes) and get back the same object, as long as the configuration does not have private
	// attributes enabled. This allows us to reuse the event processor code in ld-relay, where we may have to
	// reprocess events that have already been through the scrubbing process.

	if user.Custom != nil {
		var custom = map[string]interface{}{}
		for k, v := range *user.Custom {
			if uf.allAttributesPrivate || isPrivate[k] {
				user.PrivateAttributes = append(user.PrivateAttributes, k)
			} else {
				custom[k] = v
			}
		}
		user.Custom = &custom
	}

	if !isEmpty(user.Avatar) && (uf.allAttributesPrivate || isPrivate["avatar"]) {
		user.Avatar = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "avatar")
	}

	if !isEmpty(user.Country) && (uf.allAttributesPrivate || isPrivate["country"]) {
		user.Country = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "country")
	}

	if !isEmpty(user.Ip) && (uf.allAttributesPrivate || isPrivate["ip"]) {
		user.Ip = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "ip")
	}

	if !isEmpty(user.FirstName) && (uf.allAttributesPrivate || isPrivate["firstName"]) {
		user.FirstName = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "firstName")
	}

	if !isEmpty(user.LastName) && (uf.allAttributesPrivate || isPrivate["lastName"]) {
		user.LastName = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "lastName")
	}

	if !isEmpty(user.Name) && (uf.allAttributesPrivate || isPrivate["name"]) {
		user.Name = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "name")
	}

	if !isEmpty(user.Secondary) && (uf.allAttributesPrivate || isPrivate["secondary"]) {
		user.Secondary = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "secondary")
	}

	if !isEmpty(user.Email) && (uf.allAttributesPrivate || isPrivate["email"]) {
		user.Email = nil
		user.PrivateAttributes = append(user.PrivateAttributes, "email")
	}

	return &user
}

func isEmpty(s *string) bool {
	return s == nil || *s == ""
}
