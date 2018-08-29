package ldclient

// Segment describes a group of users
type Segment struct {
	Key      string        `json:"key" bson:"key"`
	Included []string      `json:"included" bson:"included"`
	Excluded []string      `json:"excluded" bson:"excluded"`
	Salt     string        `json:"salt" bson:"salt"`
	Rules    []SegmentRule `json:"rules" bson:"rules"`
	Version  int           `json:"version" bson:"version"`
	Deleted  bool          `json:"deleted" bson:"deleted"`
}

// GetKey returns the unique key describing a segment
func (s *Segment) GetKey() string {
	return s.Key
}

// GetVersion returns the version of the segment
func (s *Segment) GetVersion() int {
	return s.Version
}

// IsDeleted returns whether a flag has been deleted
func (s *Segment) IsDeleted() bool {
	return s.Deleted
}

// Clone returns a copy of a segment
func (s *Segment) Clone() VersionedData {
	s1 := *s
	return &s1
}

// SegmentVersionedDataKind implements VersionedDataKind and provides methods to build storage engine for segments
type SegmentVersionedDataKind struct{}

// GetNamespace returns the a unique namespace identifier for feature flag objects
func (sk SegmentVersionedDataKind) GetNamespace() string {
	return "segments"
}

// String returns the namespace
func (sk SegmentVersionedDataKind) String() string {
	return sk.GetNamespace()
}

// GetDefaultItem returns a default segment representation
func (sk SegmentVersionedDataKind) GetDefaultItem() interface{} {
	return &Segment{}
}

// MakeDeletedItem returns representation of a deleted segment
func (sk SegmentVersionedDataKind) MakeDeletedItem(key string, version int) VersionedData {
	return &Segment{Key: key, Version: version, Deleted: true}
}

// Segments is convenience variable to access an instance of SegmentVersionedDataKind
var Segments SegmentVersionedDataKind

// SegmentRule describes a set of clauses that
type SegmentRule struct {
	Id       string   `json:"id,omitempty" bson:"id,omitempty"`
	Clauses  []Clause `json:"clauses" bson:"clauses"`
	Weight   *int     `json:"weight,omitempty" bson:"weight,omitempty"`
	BucketBy *string  `json:"bucketBy,omitempty" bson:"bucketBy,omitempty"`
}

// SegmentExplanation describes a rule that determines whether a user was included in or excluded from a segment
type SegmentExplanation struct {
	Kind        string
	MatchedRule *SegmentRule
}

// ContainsUser returns whether a user belongs to the segment
func (s Segment) ContainsUser(user User) (bool, *SegmentExplanation) {
	if user.Key == nil {
		return false, nil
	}

	// Check if the user is included in the segment by key
	for _, key := range s.Included {
		if *user.Key == key {
			return true, &SegmentExplanation{Kind: "included"}
		}
	}

	// Check if the user is excluded from the segment by key
	for _, key := range s.Excluded {
		if *user.Key == key {
			return false, &SegmentExplanation{Kind: "excluded"}
		}
	}

	// Check if any of the segment rules match
	for _, rule := range s.Rules {
		if rule.MatchesUser(user, s.Key, s.Salt) {
			reason := rule
			return true, &SegmentExplanation{Kind: "rule", MatchedRule: &reason}
		}
	}

	return false, nil
}

// MatchesUser returns whether a rule applies to a user
func (r SegmentRule) MatchesUser(user User, key, salt string) bool {
	for _, clause := range r.Clauses {
		if !clause.matchesUserNoSegments(user) {
			return false
		}
	}

	// If the Weight is absent, this rule matches
	if r.Weight == nil {
		return true
	}

	// All of the clauses are met. Check to see if the user buckets in
	bucketBy := "key"

	if r.BucketBy != nil {
		bucketBy = *r.BucketBy
	}

	// Check whether the user buckets into the segment
	bucket := bucketUser(user, key, bucketBy, salt)
	weight := float32(*r.Weight) / 100000.0

	return bucket < weight
}
