package ldclient

type Segment struct {
	Key      string        `json:"key" bson:"key"`
	Included []string      `json:"included" bson:"included"`
	Excluded []string      `json:"excluded" bson:"excluded"`
	Salt     string        `json:"salt" bson:"salt"`
	Rules    []SegmentRule `json:"rules" bson:"rules"`
	Version  int           `json:"version" bson:"version"`
	Deleted  bool          `json:"deleted" bson:"deleted"`
}

func (s *Segment) GetKey() string {
	return s.Key
}

func (s *Segment) GetVersion() int {
	return s.Version
}

func (s *Segment) IsDeleted() bool {
	return s.Deleted
}

func (s *Segment) Clone() VersionedData {
	s1 := *s
	return &s1
}

type SegmentVersionedDataKind struct{}

func (sk SegmentVersionedDataKind) GetNamespace() string {
	return "segments"
}

func (sk SegmentVersionedDataKind) GetDefaultItem() interface{} {
	return &Segment{}
}

func (sk SegmentVersionedDataKind) MakeDeletedItem(key string, version int) VersionedData {
	return &Segment{Key: key, Version: version, Deleted: true}
}

var Segments SegmentVersionedDataKind

type SegmentRule struct {
	Clauses  []Clause `json:"clauses" bson:"clauses"`
	Weight   *int     `json:"weight,omitempty" bson:"weight,omitempty"`
	BucketBy *string  `json:"bucketBy,omitempty" bson:"bucketBy,omitempty"`
}

type SegmentExplanation struct {
	Kind        string
	MatchedRule *SegmentRule
}

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
