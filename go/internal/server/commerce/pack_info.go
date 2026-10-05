package commerce

import "bd2server/internal/server/wire"

type PackInfoHandler struct {
	World  attendanceHandler
	Claims *ClearPackages
}

func (h PackInfoHandler) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/PackInfo" {
		return 0, nil, false, nil
	}
	code, response, handled, err := h.World.Handle(path, request)
	if err != nil || !handled {
		return code, response, handled, err
	}
	pack, evil, err := h.Claims.RewardDBInfos()
	if err != nil {
		return code, nil, true, err
	}
	var result []byte
	err = wire.Walk(response, func(field wire.Field) error {
		if field.Number != 3 && field.Number != 4 {
			result = append(result, response[field.Start:field.End]...)
		}
		return nil
	})
	if err != nil {
		return code, nil, true, err
	}
	for _, info := range pack {
		result = wire.AppendBytes(result, 3, info)
	}
	for _, info := range evil {
		result = wire.AppendBytes(result, 4, info)
	}
	return code, result, true, nil
}
