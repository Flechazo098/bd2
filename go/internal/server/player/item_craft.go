package player

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sync"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

type itemCraftReceipt struct {
	Digest string
	Body   []byte
}
type ItemCraftService struct {
	mu         sync.Mutex
	design     *gamedata.ItemCraftDesign
	talents    *gamedata.TalentUseDesign
	store      stateio.Store
	items      *Inventory
	characters *CharacterStore
	wallet     *Wallet
	known      func(uint64) bool
	context    func() (int, bool, error)
	session    string
}

func NewItemCraftService(d *gamedata.ItemCraftDesign, talents *gamedata.TalentUseDesign, store stateio.Store, items *Inventory, characters *CharacterStore, wallet *Wallet, known func(uint64) bool) (*ItemCraftService, error) {
	if d == nil || talents == nil || store == nil || items == nil || characters == nil || wallet == nil || known == nil {
		return nil, fmt.Errorf("craft: missing dependencies")
	}
	return &ItemCraftService{design: d, talents: talents, store: store, items: items, characters: characters, wallet: wallet, known: known}, nil
}
func (s *ItemCraftService) AttachContext(context func() (int, bool, error)) { s.context = context }
func (s *ItemCraftService) BeginSession(id string)                          { s.mu.Lock(); defer s.mu.Unlock(); s.session = id }
func (s *ItemCraftService) Handle(path string, request []byte) (int, []byte, bool, error) {
	var recipes map[uint64]gamedata.ItemCraftRecipe
	var code int
	switch path {
	case "/Cooking":
		recipes = s.design.Cooking
		code = 48
	case "/Alchemy":
		recipes = s.design.Alchemy
		code = 49
	case "/AlchemyBatch":
		recipes = s.design.Alchemy
		code = 262
	default:
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(err error) (int, []byte, bool, error) { return code, nil, true, err }
	if s.session == "" || s.context == nil {
		return fail(fmt.Errorf("craft: session/context unavailable"))
	}
	values := map[int]uint64{}
	err := wire.Walk(request, func(f wire.Field) error {
		if f.Number >= 1 && f.Number <= 4 {
			if f.Type != 0 {
				return fmt.Errorf("craft: invalid scalar")
			}
			if _, exists := values[f.Number]; exists {
				return fmt.Errorf("craft: duplicate scalar")
			}
			v, _, err := wire.Varint(request, f.Number)
			if err != nil {
				return err
			}
			values[f.Number] = v
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	seq, index, recipeID, count := values[1], values[2], values[3], values[4]
	if seq == 0 || seq > math.MaxInt32 || index == 0 || index > math.MaxInt64 || recipeID == 0 || recipeID > math.MaxInt32 || count == 0 || count > math.MaxInt32 {
		return fail(fmt.Errorf("craft: invalid request"))
	}
	keyHash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", s.session, path, seq)))
	key := hex.EncodeToString(keyHash[:])
	digestHash := sha256.Sum256(request)
	digest := hex.EncodeToString(digestHash[:])
	var receipts map[string]itemCraftReceipt
	raw, err := s.store.Load("itemcraft")
	if err != nil {
		return fail(err)
	}
	if raw != nil && json.Unmarshal(raw, &receipts) != nil {
		return fail(fmt.Errorf("craft: invalid receipt state"))
	}
	if receipts == nil {
		receipts = map[string]itemCraftReceipt{}
	}
	if r, exists := receipts[key]; exists {
		if r.Digest != digest {
			return fail(fmt.Errorf("craft: changed request replay"))
		}
		return code, r.Body, true, nil
	}
	pack, battle, err := s.context()
	if err != nil {
		return fail(err)
	}
	if battle || pack <= 0 {
		return fail(fmt.Errorf("craft: unavailable in current scene"))
	}
	recipe, exists := recipes[recipeID]
	if !exists {
		return fail(fmt.Errorf("craft: unknown recipe"))
	}
	if recipe.Class == 7 && !s.known(recipeID) {
		return fail(fmt.Errorf("craft: recipe not learned"))
	}
	character, owned := s.characters.Find(index)
	if !owned || IsStoryCharacter(character) {
		return fail(fmt.Errorf("craft: unavailable producer"))
	}
	talent, exists := s.talents.Characters[character.ID]
	if !exists || talent.BannedPacks[pack] {
		return fail(fmt.Errorf("craft: producer talent blocked in pack"))
	}
	current := s.talents.Rules[[2]uint64{talent.Group, character.TalentLevel}]
	limitIndex := 0
	if recipe.Class == 7 {
		limitIndex = 1
	}
	if len(current.Values) <= limitIndex || count > uint64(current.Values[limitIndex]) {
		return fail(fmt.Errorf("craft: count exceeds talent limit"))
	}
	gain, catalyst, maximum, err := s.talents.CraftTalent(character.ID, character.TalentLevel, recipe.Class, recipe.TalentLevel, count, character.TalentExp)
	if err != nil {
		return fail(err)
	}
	materials, err := equipmentRequestItems(request, 5, path)
	if err != nil {
		return fail(err)
	}
	if path == "/AlchemyBatch" {
		gain, catalyst, maximum, err = s.prepareAlchemyBatch(character, recipe, count, materials)
	} else {
		err = validateMakingMaterials(recipe.Costs, count, materials)
		// Conversion recipes charge per produced resource, as AlchemyUI.GetNeededCurrency does.
		if err == nil && recipe.Class == 8 && recipe.Category == 2 {
			if recipe.Result.Count == 0 || catalyst > math.MaxInt32/recipe.Result.Count {
				err = fmt.Errorf("craft: catalyst overflow")
			} else {
				catalyst *= recipe.Result.Count
			}
		}
	}
	if err != nil {
		return fail(err)
	}
	if err = s.items.CanConsume(materials); err != nil {
		return fail(err)
	}
	if catalyst > 0 && !s.wallet.CanSpendCatalyst(catalyst) {
		return fail(fmt.Errorf("craft: insufficient catalyst"))
	}
	if recipe.Result.Count > math.MaxInt32/count {
		return fail(fmt.Errorf("craft: result quantity overflow"))
	}
	result := recipe.Result
	result.Count *= count
	if path == "/AlchemyBatch" {
		result.Count = count
	}
	if err = s.items.Consume(materials); err != nil {
		return fail(err)
	}
	if catalyst > 0 {
		if _, err = s.wallet.SpendCatalystOnce("itemcraft:"+key, catalyst); err != nil {
			return fail(err)
		}
	}
	granted, err := s.items.GrantOnce("itemcraft:"+key, []gamedata.BattleReward{result})
	if err != nil {
		return fail(err)
	}
	if gain > 0 {
		if _, err = s.characters.AddTalentExperience(index, gain, maximum); err != nil {
			return fail(err)
		}
	}
	var response []byte
	for _, item := range granted {
		response = wire.AppendBytes(response, 1, ItemWire(item))
	}
	if gain > 0 {
		response = wire.AppendVarint(response, 2, gain)
	}
	receipts[key] = itemCraftReceipt{Digest: digest, Body: response}
	raw, err = json.Marshal(receipts)
	if err != nil {
		return fail(err)
	}
	if err = s.store.Save("itemcraft", raw); err != nil {
		return fail(err)
	}
	return code, response, true, nil
}
