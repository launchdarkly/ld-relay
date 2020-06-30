package ldmodel

import "gopkg.in/launchdarkly/go-sdk-common.v2/lduser"

// Segment describes a group of users based on user keys and/or matching rules.
type Segment struct {
	// Key is the unique key of the user segment.
	Key string `json:"key" bson:"key"`
	// Included is a list of user keys that are always matched by this segment.
	Included []string `json:"included" bson:"included"`
	// Excluded is a list of user keys that are never matched by this segment, unless the key is also in Included.
	Excluded []string `json:"excluded" bson:"excluded"`
	// Salt is a randomized value assigned to this user segment when it is created.
	//
	// The hash function used for calculating percentage rollouts uses this as a salt to ensure that
	// rollouts are consistent within each segment but not predictable from one segment to another.
	Salt string `json:"salt" bson:"salt"`
	// Rules is a list of rules that may match a user.
	//
	// If a user is matched by a Rule, all subsequent Rules in the list are skipped. Rules are ignored
	// if the user's key is in Included or Excluded.
	Rules []SegmentRule `json:"rules" bson:"rules"`
	// Version is an integer that is incremented by LaunchDarkly every time the configuration of the segment is
	// changed.
	Version int `json:"version" bson:"version"`
	// Deleted is true if this is not actually a user segment but rather a placeholder (tombstone) for a
	// deleted segment. This is only relevant in data store implementations.
	Deleted bool `json:"deleted" bson:"deleted"`
	// preprocessedData is created by Segment.Preprocess() to speed up target matching.
	preprocessed segmentPreprocessedData
}

// GetKey returns the string key for the segment.
//
// This method exists in order to conform to interfaces used internally by the SDK.
func (s *Segment) GetKey() string {
	return s.Key
}

// GetVersion returns the version of the segment.
//
// This method exists in order to conform to interfaces used internally by the SDK.
func (s *Segment) GetVersion() int {
	return s.Version
}

// IsDeleted returns whether this is a deleted segment placeholder.
//
// This method exists in order to conform to interfaces used internally by the SDK.
func (s *Segment) IsDeleted() bool {
	return s.Deleted
}

// SegmentRule describes a set of clauses that
type SegmentRule struct {
	// ID is a randomized identifier assigned to each rule when it is created.
	ID string `json:"id,omitempty" bson:"id,omitempty"`
	// Clauses is a list of test conditions that make up the rule. These are ANDed: every Clause must
	// match in order for the SegmentRule to match.
	Clauses []Clause `json:"clauses" bson:"clauses"`
	// Weight, if non-nil, defines a percentage rollout in which only a subset of users matching this rule
	// are included in the segment. This is specified as an integer from 0 (0%) to 100000 (100%).
	Weight *int `json:"weight,omitempty" bson:"weight,omitempty"`
	// BucketBy specifies which user attribute should be used to distinguish between users in a rollout.
	//
	// The default (when BucketBy is nil) is lduser.KeyAttribute, the user's primary key. If you wish to
	// treat users with different keys as the same for rollout purposes as long as they have the same
	// "country" attribute, you would set this to "country" (lduser.CountryAttribute).
	//
	// Rollouts always take the user's "secondary key" attribute into account as well if the user has one.
	BucketBy *lduser.UserAttribute `json:"bucketBy,omitempty" bson:"bucketBy,omitempty"`
}
