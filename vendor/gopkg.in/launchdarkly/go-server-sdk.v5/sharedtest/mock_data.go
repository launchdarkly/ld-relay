//nolint:gochecknoglobals,golint,stylecheck
package sharedtest

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	intf "gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// MakeMockDataSet constructs a data set to be passed to a data store's Init method.
func MakeMockDataSet(items ...MockDataItem) []intf.StoreCollection {
	itemsColl := intf.StoreCollection{
		Kind:  MockData,
		Items: []intf.StoreKeyedItemDescriptor{},
	}
	otherItemsColl := intf.StoreCollection{
		Kind:  MockOtherData,
		Items: []intf.StoreKeyedItemDescriptor{},
	}
	for _, item := range items {
		d := intf.StoreKeyedItemDescriptor{
			Key:  item.Key,
			Item: item.ToItemDescriptor(),
		}
		if item.IsOtherKind {
			otherItemsColl.Items = append(otherItemsColl.Items, d)
		} else {
			itemsColl.Items = append(itemsColl.Items, d)
		}
	}
	return []intf.StoreCollection{itemsColl, otherItemsColl}
}

// MakeSerializedMockDataSet constructs a data set to be passed to a persistent data store's Init method.
func MakeSerializedMockDataSet(items ...MockDataItem) []intf.StoreSerializedCollection {
	itemsColl := intf.StoreSerializedCollection{
		Kind:  MockData,
		Items: []intf.StoreKeyedSerializedItemDescriptor{},
	}
	otherItemsColl := intf.StoreSerializedCollection{
		Kind:  MockOtherData,
		Items: []intf.StoreKeyedSerializedItemDescriptor{},
	}
	for _, item := range items {
		d := intf.StoreKeyedSerializedItemDescriptor{
			Key:  item.Key,
			Item: item.ToSerializedItemDescriptor(),
		}
		if item.IsOtherKind {
			otherItemsColl.Items = append(otherItemsColl.Items, d)
		} else {
			itemsColl.Items = append(itemsColl.Items, d)
		}
	}
	return []intf.StoreSerializedCollection{itemsColl, otherItemsColl}
}

func itemDescriptorsToMap(
	items []intf.StoreKeyedSerializedItemDescriptor,
) map[string]intf.StoreSerializedItemDescriptor {
	ret := make(map[string]intf.StoreSerializedItemDescriptor)
	for _, item := range items {
		ret[item.Key] = item.Item
	}
	return ret
}

// MockDataItem is a test replacement for FeatureFlag/Segment.
type MockDataItem struct {
	Key         string
	Version     int
	Deleted     bool
	Name        string
	IsOtherKind bool
}

// ToItemDescriptor converts the test item to a StoreItemDescriptor.
func (m MockDataItem) ToItemDescriptor() intf.StoreItemDescriptor {
	return intf.StoreItemDescriptor{Version: m.Version, Item: m}
}

// ToKeyedItemDescriptor converts the test item to a StoreKeyedItemDescriptor.
func (m MockDataItem) ToKeyedItemDescriptor() intf.StoreKeyedItemDescriptor {
	return intf.StoreKeyedItemDescriptor{Key: m.Key, Item: m.ToItemDescriptor()}
}

// ToSerializedItemDescriptor converts the test item to a StoreSerializedItemDescriptor.
func (m MockDataItem) ToSerializedItemDescriptor() intf.StoreSerializedItemDescriptor {
	return intf.StoreSerializedItemDescriptor{
		Version:        m.Version,
		Deleted:        m.Deleted,
		SerializedItem: MockData.Serialize(m.ToItemDescriptor()),
	}
}

// MockData is an instance of ld.StoreDataKind corresponding to MockDataItem.
var MockData = mockDataKind{isOther: false}

type mockDataKind struct {
	isOther bool
}

func (sk mockDataKind) GetName() string {
	if sk.isOther {
		return "mock2"
	}
	return "mock1"
}

func (sk mockDataKind) String() string {
	return sk.GetName()
}

func (sk mockDataKind) Serialize(item intf.StoreItemDescriptor) []byte {
	if item.Item == nil {
		return []byte(fmt.Sprintf("DELETED:%d", item.Version))
	}
	if mdi, ok := item.Item.(MockDataItem); ok {
		return []byte(fmt.Sprintf("%s,%d,%t,%s,%t", mdi.Key, mdi.Version, mdi.Deleted, mdi.Name, mdi.IsOtherKind))
	}
	return nil
}

func (sk mockDataKind) Deserialize(data []byte) (intf.StoreItemDescriptor, error) {
	if data == nil {
		return intf.StoreItemDescriptor{}.NotFound(), errors.New("tried to deserialize nil data")
	}
	s := string(data)
	if strings.HasPrefix(s, "DELETED:") {
		v, _ := strconv.Atoi(strings.TrimPrefix(s, "DELETED:"))
		return intf.StoreItemDescriptor{Version: v}, nil
	}
	fields := strings.Split(s, ",")
	if len(fields) == 5 {
		v, _ := strconv.Atoi(fields[1])
		itemIsOther := fields[4] == "true"
		if itemIsOther != sk.isOther {
			return intf.StoreItemDescriptor{}.NotFound(), errors.New("got data item of wrong kind")
		}
		isDeleted := fields[2] == "true"
		if isDeleted {
			return intf.StoreItemDescriptor{Version: v}, nil
		}
		m := MockDataItem{Key: fields[0], Version: v, Name: fields[3], IsOtherKind: itemIsOther}
		return intf.StoreItemDescriptor{Version: v, Item: m}, nil
	}
	return intf.StoreItemDescriptor{}.NotFound(), fmt.Errorf(`not a valid MockDataItem: "%s"`, data)
}

// MockOtherData is an instance of ld.StoreDataKind corresponding to another flavor of MockDataItem.
var MockOtherData = mockDataKind{isOther: true}
