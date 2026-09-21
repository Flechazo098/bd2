{-# LANGUAGE OverloadedStrings #-}

module BD2.State.Repair.CostumeOverflow
  ( CostumeFact (..)
  , BaseCostumeFact (..)
  , repairCostumeOverflow
  ) where

import BD2.State.Domain
import Data.Foldable (toList)
import Data.List (find)
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import Data.Text (Text)
import qualified Data.Text as Text
import Data.Word (Word64)
import Lens.Family2 ((&), (.~), (^.))
import Data.ProtoLens (defMessage)
import qualified Proto.Bd2.State.V1.State as V1
import qualified Proto.Bd2.State.V1.State_Fields as V1Fields
import qualified Proto.Bd2.State.V2.State as V2
import qualified Proto.Bd2.State.V2.State_Fields as V2Fields

data CostumeFact = CostumeFact
  { factCostumeId :: Word64
  , factMaxLevel :: Word64
  , factOverflowItemType :: Word64
  , factOverflowItemId :: Word64
  , factOverflowItemCount :: Word64
  }
  deriving stock (Eq, Show)

data BaseCostumeFact = BaseCostumeFact
  { baseInventoryIndex :: Word64
  , baseCostumeId :: Word64
  , baseDefaultLevel :: Word64
  }
  deriving stock (Eq, Show)

repairCostumeOverflow :: [CostumeFact] -> [BaseCostumeFact] -> V2.Snapshot -> Either [Problem] V2.Snapshot
repairCostumeOverflow facts baseFacts snapshot = do
  designs <- uniqueFacts facts
  bases <- uniqueBases baseFacts
  costumes <- traverse (capCostume designs) (toList (snapshot ^. V2Fields.roster . V2Fields.costumes))
  baseLevels <- repairBaseLevels designs bases (toList (snapshot ^. V2Fields.collection . V2Fields.baseCostumeLevels))
  grants <- traverse (repairGrant designs bases costumes) (toList (snapshot ^. V2Fields.collection . V2Fields.grants))
  wallet <- applyMileage grants (snapshot ^. V2Fields.wallet)
  pure $ snapshot
    & V2Fields.roster . V2Fields.costumes .~ costumes
    & V2Fields.collection . V2Fields.baseCostumeLevels .~ baseLevels
    & V2Fields.collection . V2Fields.grants .~ grants
    & V2Fields.wallet .~ wallet

capCostume :: Map.Map Word64 CostumeFact -> V1.Costume -> Either [Problem] V1.Costume
capCostume designs costume = do
  design <- requireDesign "repair.overflow_missing_design" designs (costume ^. V1Fields.id)
  pure $ costume & V1Fields.level .~ min (costume ^. V1Fields.level) (factMaxLevel design)

repairBaseLevels :: Map.Map Word64 CostumeFact -> Map.Map Word64 BaseCostumeFact -> [V1.NamedCount] -> Either [Problem] [V1.NamedCount]
repairBaseLevels designs bases entries = do
  repaired <- traverse repair entries
  foldl appendMissing (Right repaired) (Map.elems bases)
  where
    repair entry = case readIndex (entry ^. V1Fields.identity) >>= (`Map.lookup` bases) of
      Nothing -> Right entry
      Just base -> case Map.lookup (baseCostumeId base) designs of
        Nothing -> Right entry -- starter costume outside active gacha catalog
        Just design
          | factMaxLevel design == 0 -> Left [overflowProblem "repair.overflow_zero_max_level" "costume max level is zero" [baseCostumeId base]]
          | otherwise -> Right (entry & V1Fields.value .~ min (entry ^. V1Fields.value) (factMaxLevel design))
    appendMissing current base = do
      values <- current
      let key = Text.pack (show (baseInventoryIndex base))
      if any ((== key) . (^. V1Fields.identity)) values
        then Right values
        else case Map.lookup (baseCostumeId base) designs of
          Nothing -> Right values
          Just design
            | factMaxLevel design == 0 -> Left [overflowProblem "repair.overflow_zero_max_level" "costume max level is zero" [baseCostumeId base]]
            | baseDefaultLevel base > factMaxLevel design -> Right (values <> [defMessage & V1Fields.identity .~ key & V1Fields.value .~ factMaxLevel design])
            | otherwise -> Right values

repairGrant :: Map.Map Word64 CostumeFact -> Map.Map Word64 BaseCostumeFact -> [V1.Costume] -> V1.CollectionGrant -> Either [Problem] V1.CollectionGrant
repairGrant designs bases costumes grant = do
  (kept, generated) <- foldl repairUpgrade (Right ([], [])) (toList (grant ^. V1Fields.upgrades))
  filled <- traverse fillExchange (toList (grant ^. V1Fields.exchanges) <> generated)
  let completedUpgrades = foldl ensureUpgrade kept filled
  pure $ grant & V1Fields.upgrades .~ completedUpgrades & V1Fields.exchanges .~ filled
  where
    repairUpgrade accumulated upgrade = do
      (kept, generated) <- accumulated
      design <- requireDesign "repair.overflow_missing_upgrade_design" designs (upgrade ^. V1Fields.costumeId)
      if upgrade ^. V1Fields.after > factMaxLevel design
        then do
          exchange <- makeExchange design (upgrade ^. V1Fields.inventoryIndex) (upgrade ^. V1Fields.sortId)
          Right (kept, generated <> [exchange])
        else Right (kept <> [upgrade], generated)
    fillExchange exchange
      | exchange ^. V1Fields.inventoryIndex /= 0 = Right exchange
      | otherwise = case findInventory (exchange ^. V1Fields.originalItemId) of
          Nothing -> Left [overflowProblem "repair.overflow_missing_instance" "overflow costume has no inventory instance" [exchange ^. V1Fields.originalItemId]]
          Just index -> Right (exchange & V1Fields.inventoryIndex .~ index)
    findInventory costumeId =
      case find ((== costumeId) . (^. V1Fields.id)) costumes of
        Just costume -> Just (costume ^. V1Fields.inventoryIndex)
        Nothing -> baseInventoryIndex <$> find ((== costumeId) . baseCostumeId) (Map.elems bases)
    ensureUpgrade upgrades exchange
      | any (sameExchange exchange) upgrades = upgrades
      | otherwise = case Map.lookup (exchange ^. V1Fields.originalItemId) designs of
          Nothing -> upgrades
          Just design -> upgrades <> [defMessage
            & V1Fields.inventoryIndex .~ (exchange ^. V1Fields.inventoryIndex)
            & V1Fields.costumeId .~ (exchange ^. V1Fields.originalItemId)
            & V1Fields.before .~ factMaxLevel design
            & V1Fields.after .~ factMaxLevel design
            & V1Fields.sortId .~ (exchange ^. V1Fields.sortId)]
    sameExchange exchange upgrade = upgrade ^. V1Fields.costumeId == exchange ^. V1Fields.originalItemId
      && upgrade ^. V1Fields.sortId == exchange ^. V1Fields.sortId

makeExchange :: CostumeFact -> Word64 -> Word64 -> Either [Problem] V1.CostumeExchange
makeExchange design inventoryIndex sortId
  | factOverflowItemType design == 0 || factOverflowItemCount design == 0 =
      Left [overflowProblem "repair.overflow_missing_exchange" "costume has no overflow exchange design" [factCostumeId design]]
  | otherwise = Right $ defMessage
      & V1Fields.inventoryIndex .~ inventoryIndex
      & V1Fields.originalItemType .~ 11
      & V1Fields.originalItemId .~ factCostumeId design
      & V1Fields.originalCount .~ 1
      & V1Fields.exchangeItemType .~ factOverflowItemType design
      & V1Fields.exchangeItemId .~ factOverflowItemId design
      & V1Fields.exchangeCount .~ factOverflowItemCount design
      & V1Fields.sortId .~ sortId

applyMileage :: [V1.CollectionGrant] -> V1.Wallet -> Either [Problem] V1.Wallet
applyMileage grants wallet = foldl apply (Right wallet) grants
  where
    apply current grant = do
      value <- current
      let identity = grant ^. V1Fields.identity
          alreadyGranted = Set.member identity (Set.fromList (toList (value ^. V1Fields.grantedIdentities)))
          exchanges = toList (grant ^. V1Fields.exchanges)
      amount <- foldl addExchange (Right 0) exchanges
      if amount == 0 || alreadyGranted
        then Right value
        else if maxBound - value ^. V1Fields.mileage < amount
          then Left [overflowProblem "repair.overflow_mileage_overflow" "mileage repair would overflow uint64" [amount]]
          else Right $ value
            & V1Fields.mileage .~ (value ^. V1Fields.mileage) + amount
            & V1Fields.grantedIdentities .~ (toList (value ^. V1Fields.grantedIdentities) <> [identity])
    addExchange current exchange = do
      total <- current
      if exchange ^. V1Fields.exchangeItemType /= 20
        then Left [overflowProblem "repair.overflow_unsupported_currency" "overflow repair supports only mileage currency" [exchange ^. V1Fields.exchangeItemType]]
        else if maxBound - total < exchange ^. V1Fields.exchangeCount
          then Left [overflowProblem "repair.overflow_exchange_sum" "overflow exchange sum exceeds uint64" []]
          else Right (total + exchange ^. V1Fields.exchangeCount)

uniqueFacts :: [CostumeFact] -> Either [Problem] (Map.Map Word64 CostumeFact)
uniqueFacts = uniqueBy factCostumeId "repair.overflow_duplicate_design"

uniqueBases :: [BaseCostumeFact] -> Either [Problem] (Map.Map Word64 BaseCostumeFact)
uniqueBases = uniqueBy baseInventoryIndex "repair.overflow_duplicate_base"

uniqueBy :: Ord key => (value -> key) -> Text -> [value] -> Either [Problem] (Map.Map key value)
uniqueBy keyOf code = foldl add (Right Map.empty)
  where
    add current value = do
      values <- current
      let key = keyOf value
      if Map.member key values
        then Left [overflowProblem code "duplicate validation fact" []]
        else Right (Map.insert key value values)

requireDesign :: Text -> Map.Map Word64 CostumeFact -> Word64 -> Either [Problem] CostumeFact
requireDesign code designs costumeId = case Map.lookup costumeId designs of
  Nothing -> Left [overflowProblem code "costume has no validation fact" [costumeId]]
  Just design
    | factMaxLevel design == 0 -> Left [overflowProblem "repair.overflow_zero_max_level" "costume max level is zero" [costumeId]]
    | otherwise -> Right design

readIndex :: Text -> Maybe Word64
readIndex text = case reads (Text.unpack text) of
  [(value, "")] -> Just value
  _ -> Nothing

overflowProblem :: Text -> Text -> [Word64] -> Problem
overflowProblem code = Problem code Error ["collection", "costume_overflow"]
