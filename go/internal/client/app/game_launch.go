package app

// gameLaunchArguments asks Unity for borderless fullscreen on both supported
// desktop platforms. The LocalIdentity plug-in applies the game's native
// fullscreen routine during initialization as a second line of defence.
func gameLaunchArguments() []string {
	return []string{"-screen-fullscreen", "1", "-window-mode", "borderless"}
}
