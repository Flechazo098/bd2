package gamedata

import "fmt"

// LoadCashMailTemplates shares the decrypted GameData cache and validates the
// localized template IDs used by RewardGroupTable.MailId.
func LoadCashMailTemplates(root, version string) (map[uint64]bool, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	templates := map[uint64]bool{}
	err = readCashMetadata(db, "MailInfoTable", func(raw []byte) error {
		id, err := optionalScalar(raw, 1)
		if err != nil {
			return err
		}
		if id == 0 {
			return fmt.Errorf("gamedata: invalid cash mail template")
		}
		templates[id] = true
		return nil
	})
	return templates, err
}
