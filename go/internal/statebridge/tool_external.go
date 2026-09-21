//go:build !embedded_state_tool

package statebridge

func embeddedStateTool() (string, func(), bool, error) {
	return "", func() {}, false, nil
}
