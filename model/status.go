package model

// RequestStatus represents the lifecycle state of a resource.
type RequestStatus string

const (
	StatusPending  RequestStatus = "PENDING"
	StatusActive   RequestStatus = "ACTIVE"
	StatusDeleted  RequestStatus = "DELETED"
	StatusInactive RequestStatus = "INACTIVE"
)

// IsValid returns true if s is one of the defined RequestStatus constants.
func (s RequestStatus) IsValid() bool {
	switch s {
	case StatusPending, StatusActive, StatusDeleted, StatusInactive:
		return true
	}
	return false
}

// String implements the Stringer interface.
func (s RequestStatus) String() string { return string(s) }

// ItemType classifies the kind of data asset.
type ItemType string

const (
	ItemTypeDatabank       ItemType = "DATABANK"
	ItemTypeAIModel        ItemType = "AIMODEL"
	ItemTypeApps           ItemType = "APPS"
	ItemTypeResourceGroup  ItemType = "RESOURCE_GROUP"
)

// IsValid returns true if t is one of the defined ItemType constants.
func (t ItemType) IsValid() bool {
	switch t {
	case ItemTypeDatabank, ItemTypeAIModel, ItemTypeApps, ItemTypeResourceGroup:
		return true
	}
	return false
}

// String implements the Stringer interface.
func (t ItemType) String() string { return string(t) }
