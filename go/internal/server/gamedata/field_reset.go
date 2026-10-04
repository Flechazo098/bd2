package gamedata

import (
	"bd2server/internal/server/wire"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type FieldResetSchedule struct {
	DailyReset time.Duration
	WeeklyDay  time.Weekday
}

func LoadFieldResetSchedule(root, version string) (FieldResetSchedule, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return FieldResetSchedule{}, err
	}
	defer closeDB()
	var raw []byte
	if err = db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return FieldResetSchedule{}, err
	}
	times, found, err := wire.Bytes(raw, 23)
	if err != nil || !found {
		return FieldResetSchedule{}, fmt.Errorf("gamedata: missing field daily reset time")
	}
	parts := strings.Split(string(times), ":")
	if len(parts) != 3 {
		return FieldResetSchedule{}, fmt.Errorf("gamedata: invalid daily reset time")
	}
	values := [3]int{}
	for i, p := range parts {
		values[i], err = strconv.Atoi(p)
		if err != nil || values[i] < 0 || (i == 0 && values[i] > 23) || (i > 0 && values[i] > 59) {
			return FieldResetSchedule{}, fmt.Errorf("gamedata: invalid daily reset time")
		}
	}
	day, _, err := wire.Varint(raw, 97)
	if err != nil || day > 6 {
		return FieldResetSchedule{}, fmt.Errorf("gamedata: invalid weekly reset day")
	}
	return FieldResetSchedule{DailyReset: time.Duration(values[0])*time.Hour + time.Duration(values[1])*time.Minute + time.Duration(values[2])*time.Second, WeeklyDay: time.Weekday(day)}, nil
}

// Period follows client TimerManager's UTC+9 conversion and GameDefault reset
// settings. ResetByEvent needs the event's own lifecycle, never a daily fallback.
func (s FieldResetSchedule) Period(reset int, now time.Time) (string, error) {
	if reset == 1 {
		return "once", nil
	}
	if reset != 0 && reset != 3 {
		return "", fmt.Errorf("gamedata: missing field event reset lifecycle")
	}
	shifted := now.UTC().Add(9*time.Hour - s.DailyReset)
	if reset == 3 {
		days := (int(shifted.Weekday()) - int(s.WeeklyDay) + 7) % 7
		shifted = shifted.AddDate(0, 0, -days)
	}
	return shifted.Format("2006-01-02"), nil
}
