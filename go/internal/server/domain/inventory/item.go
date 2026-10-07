package inventory

type Item struct {
	InvenIndex    uint64         `json:"inven_index"`
	ID            uint64         `json:"id"`
	Type          uint64         `json:"type"`
	Count         uint64         `json:"count"`
	KeepFlag      uint64         `json:"keep_flag,omitempty"`
	TimeValue     uint64         `json:"time_value,omitempty"`
	ExpiryTime    uint64         `json:"expiry_time,omitempty"`
	Pictorialbook *ItemPictorial `json:"pictorialbook,omitempty"`
	SortID        uint64         `json:"sort_id,omitempty"`
	UseCount      uint64         `json:"use_count,omitempty"`
}

type ItemPictorial struct {
	ID      uint64 `json:"id"`
	GroupID uint64 `json:"group_id"`
}
