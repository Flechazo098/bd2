package commerce

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"bd2server/internal/server/gamedata"
)

type CashMailIssuer interface {
	IssueCashOnce(string, uint64, []gamedata.Reward, time.Time) error
}

func (e *EntitlementEconomy) AttachCashMail(issuer CashMailIssuer) error {
	if issuer == nil {
		return fmt.Errorf("commerce: cash mail issuer unavailable")
	}
	if _, ok := e.graph.(interface {
		ResolveDelivery([]gamedata.BattleReward) (gamedata.CashDelivery, error)
	}); !ok {
		return fmt.Errorf("commerce: cash delivery resolver unavailable")
	}
	e.mail = issuer
	return nil
}

// ApplyPurchase selects rewards once, activates direct entitlements, and issues
// durable cash mail. All writes run in the enclosing account transaction.
func (e *EntitlementEconomy) ApplyPurchase(identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.mail == nil {
		return nil, fmt.Errorf("commerce: cash mail issuer unavailable")
	}
	definition, _ := json.Marshal(struct{ Costs, Rewards []gamedata.Reward }{costs, rewards})
	digest := sha256.Sum256(definition)
	key := identity + ":delivery"
	state, err := e.load()
	if err != nil {
		return nil, err
	}
	if receipt, ok := state.Receipts[key]; ok {
		if !bytes.Equal(receipt.Definition, digest[:]) {
			return nil, fmt.Errorf("commerce: delivery identity reused")
		}
		return append([]byte(nil), receipt.Bundle...), nil
	}
	resolver, ok := e.graph.(interface {
		ResolveDelivery([]gamedata.BattleReward) (gamedata.CashDelivery, error)
	})
	if !ok {
		return nil, fmt.Errorf("commerce: missing cash delivery resolver")
	}
	input := make([]gamedata.BattleReward, len(rewards))
	for i, r := range rewards {
		input[i] = gamedata.BattleReward(r)
	}
	plan, err := resolver.ResolveDelivery(input)
	if err != nil {
		return nil, err
	}
	direct := make([]gamedata.Reward, len(plan.Direct))
	for i, r := range plan.Direct {
		direct[i] = gamedata.Reward(r)
	}
	var mailed []gamedata.BattleReward
	for _, mail := range plan.Mail {
		mailed = append(mailed, mail.Rewards...)
	}
	bundle, err := e.applyPrepared(identity+":direct", costs, direct, true, mailed)
	if err != nil {
		return nil, err
	}
	for i, mail := range plan.Mail {
		attachments := make([]gamedata.Reward, len(mail.Rewards))
		for j, r := range mail.Rewards {
			attachments[j] = gamedata.Reward(r)
		}
		if err := e.mail.IssueCashOnce(fmt.Sprintf("%s:%d", identity, i), mail.TemplateID, attachments, e.now()); err != nil {
			return nil, err
		}
	}
	state, err = e.load()
	if err != nil {
		return nil, err
	}
	state.Receipts[key] = entitlementReceipt{Definition: digest[:], Bundle: append([]byte(nil), bundle...)}
	if err = e.save(state); err != nil {
		return nil, err
	}
	return bundle, nil
}
