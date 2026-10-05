package events

// SkinHandler restores skin ownership through the client's dedicated query.
type SkinHandler struct{ Economy *Economy }

func (h SkinHandler) Handle(path string, request []byte) (int, []byte, bool, error) {
	if code, response, handled, err := h.Economy.OwnedItemInfo(path, request); handled || err != nil {
		return code, response, handled, err
	}
	return h.Economy.PrestigeSkinInfo(path, request)
}
