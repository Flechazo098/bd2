{-# LANGUAGE OverloadedStrings #-}

module Main (main) where

import BD2.State.Framing
import BD2.State.Domain (Problem (problemCode), CharacterRedirect (..))
import BD2.State.Migrate.V1ToV2
import BD2.State.Repair.DuplicateCharacters
import BD2.State.Repair.CostumeOverflow
import BD2.State.Repair.GachaProgress
import BD2.State.Repair.NextIndices
import BD2.State.Repair.Pipeline
import BD2.State.Repair.StepUpProgress
import BD2.State.Validate
import Data.Foldable (toList)
import Data.String (fromString)
import Data.ProtoLens (defMessage)
import Lens.Family2 ((&), (.~), (^.))
import qualified Proto.Bd2.State.V1.State as State
import qualified Proto.Bd2.State.V1.State_Fields as StateFields
import qualified Proto.Bd2.State.V2.State as StateV2
import qualified Proto.Bd2.State.V2.State_Fields as StateV2Fields
import qualified Proto.Bd2.State.Control.V2.Control as ControlV2
import qualified Proto.Bd2.State.Control.V2.Control_Fields as ControlV2Fields
import Control.Monad (unless)
import System.Exit (exitFailure)
import Test.QuickCheck

main :: IO ()
main = do
  results <- mapM quickCheckResult
    [ property prop_frameRoundTrip
    , property prop_validInventoryNextIndex
    , property prop_consumedGrantedItemIsValid
    , property prop_unissuedGrantIndexIsRejected
    , property prop_equipmentGrantRequiresOwnedInstance
    , property prop_equippedCharacterRequiresOwnedCharacter
    , property prop_migrationProducesValidV2
    , property prop_migrationPreservesIdentityAndOrigin
    , property prop_migrationPreservesEconomy
    , property prop_v2SchemaRoundTrip
    , property prop_migrationRejectsDuplicateCharacter
    , property prop_currentMigrationIsIdempotent
    , property prop_duplicateRepairRedirectsOwnersAndPreservesEconomy
    , property prop_duplicateRepairRejectsAmbiguousBase
    , property prop_nextIndexRepairIsMonotoneAndIdempotent
    , property prop_nextIndexRepairUsesHistoricalGrants
    , property prop_nextIndexRepairRejectsOverflow
    , property prop_stepUpRepairUsesConsecutiveGrants
    , property prop_stepUpRepairPreservesHigherStoredProgress
    , property prop_stepUpRejectsWrongGameData
    , property prop_gachaReplayOrdersDrawsAndIsIdempotent
    , property prop_costumeOverflowIsAtomicAndIdempotent
    , property prop_fullRepairPipelineProducesValidCurrent
    ]
  unless (all isSuccess results) exitFailure

prop_frameRoundTrip :: Property
prop_frameRoundTrip =
  let snapshot :: State.Snapshot
      snapshot = defMessage & StateFields.formatVersion .~ 1 & StateFields.clientVersion .~ "2.34.13" & StateFields.gameDataVersion .~ "test"
   in decodeDelimited (encodeDelimited snapshot) === Right snapshot

prop_validInventoryNextIndex :: Positive Word -> Bool
prop_validInventoryNextIndex (Positive value) =
  let index = fromIntegral (value `mod` 1000000 + 1)
      item :: State.Item
      item = defMessage & StateFields.inventoryIndex .~ index & StateFields.id .~ 1 & StateFields.type' .~ 1 & StateFields.count .~ 1
      inventory :: State.Inventory
      inventory = defMessage & StateFields.nextIndex .~ index + 1 & StateFields.items .~ [item]
      snapshot :: State.Snapshot
      snapshot = defMessage & StateFields.formatVersion .~ 1 & StateFields.clientVersion .~ "2.34.13" & StateFields.gameDataVersion .~ "test" & StateFields.inventory .~ inventory
      (problems, valid) = validateSnapshot snapshot
   in null problems && case valid of Just _ -> True; Nothing -> False

-- A mail reward can be spent later: grant_items records issuance, not a
-- foreign key into currently owned items.
prop_consumedGrantedItemIsValid :: Positive Word -> Bool
prop_consumedGrantedItemIsValid (Positive value) =
  let index = fromIntegral (value `mod` 1000000 + 1)
      grant :: State.IndexedGrant
      grant = defMessage & StateFields.identity .~ "mail:test:items" & StateFields.inventoryIndices .~ [index]
      inventory :: State.Inventory
      inventory = defMessage & StateFields.nextIndex .~ index + 1
        & StateFields.grantedIdentities .~ ["mail:test:items"] & StateFields.grantItems .~ [grant]
      snapshot :: State.Snapshot
      snapshot = defMessage & StateFields.formatVersion .~ 1 & StateFields.clientVersion .~ "2.34.13"
        & StateFields.gameDataVersion .~ "test" & StateFields.inventory .~ inventory
      (problems, valid) = validateSnapshot snapshot
   in null problems && case valid of Just _ -> True; Nothing -> False

prop_unissuedGrantIndexIsRejected :: Positive Word -> Bool
prop_unissuedGrantIndexIsRejected (Positive value) =
  let index = fromIntegral (value `mod` 1000000 + 1)
      grant :: State.IndexedGrant
      grant = defMessage & StateFields.identity .~ "mail:test:items" & StateFields.inventoryIndices .~ [index]
      inventory :: State.Inventory
      inventory = defMessage & StateFields.nextIndex .~ index
        & StateFields.grantedIdentities .~ ["mail:test:items"] & StateFields.grantItems .~ [grant]
      snapshot :: State.Snapshot
      snapshot = defMessage & StateFields.formatVersion .~ 1 & StateFields.clientVersion .~ "2.34.13"
        & StateFields.gameDataVersion .~ "test" & StateFields.inventory .~ inventory
      (problems, _) = validateSnapshot snapshot
   in any ((== "inventory.grant_invalid_index") . problemCode) problems

prop_equipmentGrantRequiresOwnedInstance :: Positive Word -> Bool
prop_equipmentGrantRequiresOwnedInstance (Positive value) =
  let index = fromIntegral (value `mod` 1000000 + 1)
      grant :: State.NamedIndex
      grant = defMessage & StateFields.identity .~ "quest:test:equip" & StateFields.inventoryIndex .~ index
      equipment :: State.EquipmentInventory
      equipment = defMessage & StateFields.nextIndex .~ index + 1 & StateFields.grants .~ [grant]
      snapshot :: State.Snapshot
      snapshot = defMessage & StateFields.formatVersion .~ 1 & StateFields.clientVersion .~ "2.34.13"
        & StateFields.gameDataVersion .~ "test" & StateFields.equipment .~ equipment
      (problems, _) = validateSnapshot snapshot
   in any ((== "equipment.grant_missing_equipment") . problemCode) problems

prop_equippedCharacterRequiresOwnedCharacter :: Positive Word -> Bool
prop_equippedCharacterRequiresOwnedCharacter (Positive value) =
  let index = fromIntegral (value `mod` 1000000 + 1)
      entry :: State.Equipment
      entry = defMessage & StateFields.inventoryIndex .~ index & StateFields.id .~ 1
        & StateFields.useChar .~ (index + 1000000)
      equipment :: State.EquipmentInventory
      equipment = defMessage & StateFields.nextIndex .~ index + 1 & StateFields.equipment .~ [entry]
      snapshot :: State.Snapshot
      snapshot = defMessage & StateFields.formatVersion .~ 1 & StateFields.clientVersion .~ "2.34.13"
        & StateFields.gameDataVersion .~ "test" & StateFields.equipment .~ equipment
      (problems, _) = validateSnapshot snapshot
   in any ((== "equipment.unknown_user") . problemCode) problems

prop_migrationProducesValidV2 :: Positive Word -> Bool
prop_migrationProducesValidV2 value =
  case migrateV1ToV2 (migrationFixture value) of
    Left _ -> False
    Right target -> null (validateV2 target)

prop_migrationPreservesIdentityAndOrigin :: Positive Word -> Bool
prop_migrationPreservesIdentityAndOrigin value =
  let source = migrationFixture value
      baseIndex = source ^. StateFields.characters . StateFields.characters
      acquiredIndex = source ^. StateFields.collection . StateFields.characters
   in case migrateV1ToV2 source of
        Left _ -> False
        Right target ->
          let records = toList (target ^. StateV2Fields.roster . StateV2Fields.characters)
           in map (^. StateV2Fields.character . StateFields.inventoryIndex) records
                == map (^. StateFields.inventoryIndex) (toList baseIndex <> toList acquiredIndex)
                && map (^. StateV2Fields.origin) records
                == [StateV2.CHARACTER_ORIGIN_BASE, StateV2.CHARACTER_ORIGIN_ACQUIRED]

prop_migrationPreservesEconomy :: Positive Word -> Bool
prop_migrationPreservesEconomy value =
  let source = migrationFixture value
   in case migrateV1ToV2 source of
        Left _ -> False
        Right target -> target ^. StateV2Fields.wallet == source ^. StateFields.wallet

prop_v2SchemaRoundTrip :: Positive Word -> Bool
prop_v2SchemaRoundTrip value =
  case migrateV1ToV2 (migrationFixture value) of
    Left _ -> False
    Right target -> decodeDelimited (encodeDelimited target) == Right target

prop_migrationRejectsDuplicateCharacter :: Positive Word -> Bool
prop_migrationRejectsDuplicateCharacter value =
  let source = migrationFixture value
      base = toList (source ^. StateFields.characters . StateFields.characters)
      damaged = source & StateFields.characters . StateFields.characters .~ (base <> base)
   in case migrateV1ToV2 damaged of
        Left problems -> any ((== "characters.duplicate_index") . problemCode) problems
        Right _ -> False

prop_currentMigrationIsIdempotent :: Positive Word -> Bool
prop_currentMigrationIsIdempotent value =
  case migrateV1ToV2 (migrationFixture value) of
    Left _ -> False
    Right target -> migrateCurrentV2 target == Right target

prop_duplicateRepairRedirectsOwnersAndPreservesEconomy :: Positive Word -> Bool
prop_duplicateRepairRedirectsOwnersAndPreservesEconomy value =
  case migrateV1ToV2 (migrationFixture value) of
    Left _ -> False
    Right original ->
      let records = toList (original ^. StateV2Fields.roster . StateV2Fields.characters)
          base = head records
          acquired = records !! 1
          duplicateIndex = acquired ^. StateV2Fields.character . StateFields.inventoryIndex
          baseIndex = base ^. StateV2Fields.character . StateFields.inventoryIndex
          duplicate = acquired & StateV2Fields.character . StateFields.id .~ (base ^. StateV2Fields.character . StateFields.id)
          grant :: State.CollectionGrant
          grant = defMessage & StateFields.identity .~ "old" & StateFields.characterIndices .~ [duplicateIndex]
          slot :: State.DeckSlot
          slot = defMessage & StateFields.characterIndex .~ duplicateIndex & StateFields.slot .~ 1
          equipment :: State.Equipment
          equipment = defMessage & StateFields.inventoryIndex .~ 1 & StateFields.id .~ 1 & StateFields.useChar .~ duplicateIndex
          damaged = original
            & StateV2Fields.roster . StateV2Fields.characters .~ [base, duplicate]
            & StateV2Fields.collection . StateV2Fields.grants .~ [grant]
            & StateV2Fields.deck . StateFields.battle .~ [slot]
            & StateV2Fields.equipment . StateFields.equipment .~ [equipment]
            & StateV2Fields.equipment . StateFields.nextIndex .~ 2
       in case repairDuplicateCharacters damaged of
            Left _ -> False
            Right (repaired, redirects) ->
              length redirects == 1
                && redirectFromIndex (head redirects) == duplicateIndex
                && redirectToIndex (head redirects) == baseIndex
                && length (toList (repaired ^. StateV2Fields.roster . StateV2Fields.characters)) == 1
                && head (toList (repaired ^. StateV2Fields.roster . StateV2Fields.costumes)) ^. StateFields.useChar == baseIndex
                && head (toList (repaired ^. StateV2Fields.deck . StateFields.battle)) ^. StateFields.characterIndex == baseIndex
                && head (toList (repaired ^. StateV2Fields.equipment . StateFields.equipment)) ^. StateFields.useChar == baseIndex
                && null (toList (head (toList (repaired ^. StateV2Fields.collection . StateV2Fields.grants)) ^. StateFields.characterIndices))
                && repaired ^. StateV2Fields.wallet == original ^. StateV2Fields.wallet
                && null (validateV2 repaired)
                && case repairDuplicateCharacters repaired of
                  Right (same, []) -> same == repaired
                  _ -> False

prop_duplicateRepairRejectsAmbiguousBase :: Positive Word -> Bool
prop_duplicateRepairRejectsAmbiguousBase value =
  case migrateV1ToV2 (migrationFixture value) of
    Left _ -> False
    Right original ->
      let base = head (toList (original ^. StateV2Fields.roster . StateV2Fields.characters))
          damaged = original & StateV2Fields.roster . StateV2Fields.characters .~ [base, base]
       in case repairDuplicateCharacters damaged of
            Left problems -> any ((== "repair.duplicate_base_character") . problemCode) problems
            Right _ -> False

prop_nextIndexRepairIsMonotoneAndIdempotent :: Positive Word -> Bool
prop_nextIndexRepairIsMonotoneAndIdempotent value =
  case migrateV1ToV2 (migrationFixture value) of
    Left _ -> False
    Right target ->
      let stale = target & StateV2Fields.roster . StateV2Fields.nextAcquiredCharacterIndex .~ 1
            & StateV2Fields.roster . StateV2Fields.nextCostumeIndex .~ 1
       in case repairNextIndices stale of
            Left _ -> False
            Right repaired ->
              repaired ^. StateV2Fields.roster . StateV2Fields.nextAcquiredCharacterIndex
                == max 920000001 (target ^. StateV2Fields.roster . StateV2Fields.nextAcquiredCharacterIndex)
                && repaired ^. StateV2Fields.roster . StateV2Fields.nextCostumeIndex
                == max 930000001 (target ^. StateV2Fields.roster . StateV2Fields.nextCostumeIndex)
                && repairNextIndices repaired == Right repaired
                && repaired ^. StateV2Fields.wallet == stale ^. StateV2Fields.wallet

prop_nextIndexRepairUsesHistoricalGrants :: Positive Word -> Bool
prop_nextIndexRepairUsesHistoricalGrants (Positive value) =
  let issuedIndex = fromIntegral (value `mod` 1000000 + 900000001)
      grant :: State.IndexedGrant
      grant = defMessage & StateFields.identity .~ "spent-mail" & StateFields.inventoryIndices .~ [issuedIndex]
      snapshot :: StateV2.Snapshot
      snapshot = defMessage & StateV2Fields.inventory . StateFields.grantItems .~ [grant]
   in case repairNextIndices snapshot of
        Left _ -> False
        Right repaired -> repaired ^. StateV2Fields.inventory . StateFields.nextIndex == issuedIndex + 1

prop_nextIndexRepairRejectsOverflow :: Bool
prop_nextIndexRepairRejectsOverflow =
  let item :: State.Item
      item = defMessage & StateFields.inventoryIndex .~ maxBound
      snapshot :: StateV2.Snapshot
      snapshot = defMessage & StateV2Fields.inventory . StateFields.items .~ [item]
   in case repairNextIndices snapshot of
        Left problems -> any ((== "inventory.next_index_overflow") . problemCode) problems
        Right _ -> False

prop_stepUpRepairUsesConsecutiveGrants :: Positive Word -> Bool
prop_stepUpRepairUsesConsecutiveGrants (Positive value) =
  let first = fromIntegral (value `mod` 1000000 + 8100118)
      design = StepUpFact "test" 29 [first, first + 1, first + 2, first + 3]
      grant identity = (defMessage :: State.CollectionGrant) & StateFields.identity .~ identity
      fixture :: StateV2.Snapshot
      fixture = defMessage & StateV2Fields.gameDataVersion .~ "test"
        & StateV2Fields.collection . StateV2Fields.grants .~
        [ grant ("regular-gacha:" <> fromString (show first) <> ":session:a")
        , grant ("regular-gacha:" <> fromString (show (first + 1)) <> ":seq:2")
        , grant ("regular-gacha:" <> fromString (show (first + 3)) <> ":seq:4")
        ]
   in case repairStepUpProgress design fixture of
        Left _ -> False
        Right (repaired, count) ->
          count == 2 && case repairStepUpProgress design repaired of
            Right (same, countAgain) -> same == repaired && countAgain == 2
            _ -> False

prop_stepUpRepairPreservesHigherStoredProgress :: Positive Word -> Bool
prop_stepUpRepairPreservesHigherStoredProgress (Positive value) =
  let first = fromIntegral (value `mod` 1000000 + 8100118)
      design = StepUpFact "test" 29 [first, first + 1, first + 2, first + 3]
      stored = (defMessage :: State.NamedCount) & StateFields.identity .~ "29" & StateFields.value .~ 3
      fixture :: StateV2.Snapshot
      fixture = defMessage & StateV2Fields.gameDataVersion .~ "test"
        & StateV2Fields.collection . StateV2Fields.stepUpProgress .~ [stored]
   in repairStepUpProgress design fixture == Right (fixture, 3)

prop_stepUpRejectsWrongGameData :: Positive Word -> Bool
prop_stepUpRejectsWrongGameData (Positive value) =
  let gachaId = fromIntegral (value `mod` 1000000 + 8100118)
      fixture :: StateV2.Snapshot
      fixture = defMessage & StateV2Fields.gameDataVersion .~ "20260910162539"
   in case repairStepUpProgress (StepUpFact "wrong" 29 [gachaId]) fixture of
        Left problems -> any ((== "repair.step_up_game_data_version") . problemCode) problems
        Right _ -> False

prop_gachaReplayOrdersDrawsAndIsIdempotent :: Positive Word -> Bool
prop_gachaReplayOrdersDrawsAndIsIdempotent (Positive value) =
  let gachaId = fromIntegral (value `mod` 1000000 + 100)
      groupId = gachaId + 1000
      fiveId = gachaId + 2000
      threeId = gachaId + 3000
      grant identity costumeId = (defMessage :: State.CollectionGrant)
        & StateFields.identity .~ identity & StateFields.viewCostumeIds .~ [costumeId]
      fixture :: StateV2.Snapshot
      fixture = defMessage & StateV2Fields.collection . StateV2Fields.grants .~
        [ grant ("regular-gacha:" <> fromString (show gachaId) <> ":session:s:seq:2") threeId
        , grant ("regular-gacha:" <> fromString (show gachaId) <> ":session:s:seq:1") fiveId
        ]
      groups = [GachaGroupFact gachaId groupId 8 10]
      grades = [CostumeGradeFact fiveId 5, CostumeGradeFact threeId 3]
   in case repairGachaProgress groups grades fixture of
        Left _ -> False
        Right repaired ->
          let users = toList (repaired ^. StateV2Fields.collection . StateV2Fields.gachaUsers)
              fixed = toList (repaired ^. StateV2Fields.collection . StateV2Fields.gachaFixed)
              user = head users ^. StateFields.user
              fixedCounts = map ((^. StateFields.count) . (^. StateFields.fixed)) fixed
           in length users == 1
                && user ^. StateFields.groupId == groupId
                && user ^. StateFields.totalBuyCount == 2
                && user ^. StateFields.point == 20
                && fixedCounts == [1, 1]
                && repaired ^. StateV2Fields.collection . StateV2Fields.gachaApplied
                  == [ "regular-gacha:" <> fromString (show gachaId) <> ":session:s:seq:1"
                     , "regular-gacha:" <> fromString (show gachaId) <> ":session:s:seq:2" ]
                && repairGachaProgress groups grades repaired == Right repaired

prop_costumeOverflowIsAtomicAndIdempotent :: Positive Word -> Bool
prop_costumeOverflowIsAtomicAndIdempotent value =
  case migrateV1ToV2 (migrationFixture value) of
    Left _ -> False
    Right target ->
      let costume = head (toList (target ^. StateV2Fields.roster . StateV2Fields.costumes))
          inventoryIndex = costume ^. StateFields.inventoryIndex
          overCostume = costume & StateFields.level .~ 7
          upgrade = (defMessage :: State.CostumeUpgrade)
            & StateFields.inventoryIndex .~ inventoryIndex & StateFields.costumeId .~ 20
            & StateFields.before .~ 5 & StateFields.after .~ 6 & StateFields.sortId .~ 3
          grant = (defMessage :: State.CollectionGrant)
            & StateFields.identity .~ "overflow:test" & StateFields.upgrades .~ [upgrade]
          damaged = target
            & StateV2Fields.roster . StateV2Fields.costumes .~ [overCostume]
            & StateV2Fields.collection . StateV2Fields.grants .~ [grant]
          facts = [CostumeFact 20 5 20 0 10]
       in case repairCostumeOverflow facts [] damaged of
            Left _ -> False
            Right repaired ->
              let resultGrant = head (toList (repaired ^. StateV2Fields.collection . StateV2Fields.grants))
               in head (toList (repaired ^. StateV2Fields.roster . StateV2Fields.costumes)) ^. StateFields.level == 5
                    && length (toList (resultGrant ^. StateFields.exchanges)) == 1
                    && length (toList (resultGrant ^. StateFields.upgrades)) == 1
                    && repaired ^. StateV2Fields.wallet . StateFields.mileage == 10
                    && repaired ^. StateV2Fields.wallet . StateFields.grantedIdentities == ["overflow:test"]
                    && repairCostumeOverflow facts [] repaired == Right repaired

prop_fullRepairPipelineProducesValidCurrent :: Positive Word -> Bool
prop_fullRepairPipelineProducesValidCurrent value =
  let source = migrationFixture value
      costume = (defMessage :: ControlV2.CostumeFact)
        & ControlV2Fields.costumeId .~ 20 & ControlV2Fields.grade .~ 3
        & ControlV2Fields.maxLevel .~ 5 & ControlV2Fields.overflowItemType .~ 20
        & ControlV2Fields.overflowItemCount .~ 2
      context = (defMessage :: ControlV2.ValidationContext)
        & ControlV2Fields.gameDataVersion .~ "test" & ControlV2Fields.costumes .~ [costume]
      request = (defMessage :: ControlV2.RepairRequest)
        & ControlV2Fields.bridgeApiVersion .~ 2 & ControlV2Fields.source .~ source & ControlV2Fields.context .~ context
   in case repairV1ToCurrent request of
        Left _ -> False
        Right (target, _) -> null (validateV2 target)

migrationFixture :: Positive Word -> State.Snapshot
migrationFixture (Positive value) =
  let baseIndex = fromIntegral (value `mod` 1000000 + 1)
      acquiredIndex = baseIndex + 1000000
      costumeIndex = acquiredIndex + 1000000
      baseCharacter :: State.Character
      baseCharacter = defMessage & StateFields.inventoryIndex .~ baseIndex & StateFields.id .~ 1
      acquiredCharacter :: State.Character
      acquiredCharacter = defMessage & StateFields.inventoryIndex .~ acquiredIndex & StateFields.id .~ 2
      costume :: State.Costume
      costume = defMessage & StateFields.inventoryIndex .~ costumeIndex & StateFields.id .~ 20 & StateFields.useChar .~ acquiredIndex
      characters :: State.CharacterState
      characters = defMessage & StateFields.clientVersion .~ "2.34.13" & StateFields.characters .~ [baseCharacter]
      collection :: State.Collection
      collection = defMessage & StateFields.clientVersion .~ "2.34.13"
        & StateFields.nextCharacterIndex .~ acquiredIndex + 1 & StateFields.nextCostumeIndex .~ costumeIndex + 1
        & StateFields.characters .~ [acquiredCharacter] & StateFields.costumes .~ [costume]
      wallet :: State.Wallet
      wallet = defMessage & StateFields.clientVersion .~ "2.34.13"
        & StateFields.gold .~ fromIntegral value & StateFields.freeJewelry .~ fromIntegral value + 7
   in defMessage & StateFields.formatVersion .~ 1 & StateFields.clientVersion .~ "2.34.13"
        & StateFields.gameDataVersion .~ "test" & StateFields.characters .~ characters
        & StateFields.collection .~ collection & StateFields.wallet .~ wallet
