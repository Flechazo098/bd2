{-# LANGUAGE OverloadedStrings #-}

module BD2.State.Migrate.V1ToV2
  ( migrateV1ToV2
  , migrateV1ToV2ForRepair
  , migrateCurrentV2
  , validateV2
  ) where

import BD2.State.Domain
import BD2.State.Validate (validateSnapshot)
import Data.Foldable (toList)
import Data.List (group, sort)
import Data.ProtoLens (defMessage)
import qualified Data.Set as Set
import Data.Text (Text)
import qualified Data.Text as Text
import Data.Word (Word64)
import Lens.Family2 ((&), (.~), (^.))
import qualified Proto.Bd2.State.V1.State as V1
import qualified Proto.Bd2.State.V1.State_Fields as V1Fields
import qualified Proto.Bd2.State.V2.State as V2
import qualified Proto.Bd2.State.V2.State_Fields as V2Fields

-- | Pure schema migration. It does not repair or allocate identities: an
-- invalid V1 snapshot is rejected, and every durable entity/index is copied.
migrateV1ToV2 :: V1.Snapshot -> Either [Problem] V2.Snapshot
migrateV1ToV2 source =
  case validateSnapshot source of
    (problems, Nothing) -> Left problems
    (_, Just valid) ->
      let target = buildTarget valid
          targetProblems = validateV2 target
       in if null targetProblems then Right target else Left targetProblems

-- Repair entry accepts only stale allocator cursors; every other V1 error is
-- still fatal. The repair pipeline must advance these cursors before V2 is
-- validated or returned.
migrateV1ToV2ForRepair :: V1.Snapshot -> Either [Problem] V2.Snapshot
migrateV1ToV2ForRepair source =
  let (problems, _) = validateSnapshot source
      fatal = filter (not . repairableV1Problem . problemCode) problems
      collection = source ^. V1Fields.collection
      valid = ValidSnapshot source
        (toList (source ^. V1Fields.characters . V1Fields.characters))
        (toList (collection ^. V1Fields.characters))
        (toList (collection ^. V1Fields.costumes))
   in if null fatal then Right (buildTarget valid) else Left fatal

repairableV1Problem :: Text -> Bool
repairableV1Problem code = code `elem`
  [ "inventory.next_index"
  , "inventory.grant_invalid_index"
  , "equipment.next_index"
  , "collection.next_character_index"
  , "collection.next_costume_index"
  ]

-- | The current-version migration is deliberately the identity after
-- validation. This is the executable idempotence law used at startup later.
migrateCurrentV2 :: V2.Snapshot -> Either [Problem] V2.Snapshot
migrateCurrentV2 snapshot =
  case validateV2 snapshot of
    [] -> Right snapshot
    problems -> Left problems

buildTarget :: ValidSnapshot -> V2.Snapshot
buildTarget valid =
  let source = validatedSource valid
      oldCollection = source ^. V1Fields.collection
      baseCharacters = map (characterRecord V2.CHARACTER_ORIGIN_BASE) (validatedBaseCharacters valid)
      acquiredCharacters = map (characterRecord V2.CHARACTER_ORIGIN_ACQUIRED) (validatedAcquiredCharacters valid)
      roster =
        defMessage
          & V2Fields.nextAcquiredCharacterIndex .~ (oldCollection ^. V1Fields.nextCharacterIndex)
          & V2Fields.nextCostumeIndex .~ (oldCollection ^. V1Fields.nextCostumeIndex)
          & V2Fields.characters .~ (baseCharacters <> acquiredCharacters)
          & V2Fields.costumes .~ validatedCostumes valid
      collection =
        defMessage
          & V2Fields.latestPreview .~ toList (oldCollection ^. V1Fields.latestPreview)
          & V2Fields.previewEventIndex .~ (oldCollection ^. V1Fields.previewEventIndex)
          & V2Fields.previewLocked .~ (oldCollection ^. V1Fields.previewLocked)
          & V2Fields.baseCostumeLevels .~ toList (oldCollection ^. V1Fields.baseCostumeLevels)
          & V2Fields.gachaSelections .~ toList (oldCollection ^. V1Fields.gachaSelections)
          & V2Fields.stepUpProgress .~ toList (oldCollection ^. V1Fields.stepUpProgress)
          & V2Fields.gachaUsers .~ toList (oldCollection ^. V1Fields.gachaUsers)
          & V2Fields.gachaFixed .~ toList (oldCollection ^. V1Fields.gachaFixed)
          & V2Fields.gachaApplied .~ toList (oldCollection ^. V1Fields.gachaApplied)
          & V2Fields.gachaPointExchanges .~ toList (oldCollection ^. V1Fields.gachaPointExchanges)
          & V2Fields.gachaCountCorrected .~ (oldCollection ^. V1Fields.gachaCountCorrected)
          & V2Fields.grants .~ toList (oldCollection ^. V1Fields.grants)
   in defMessage
        & V2Fields.formatVersion .~ 2
        & V2Fields.clientVersion .~ (source ^. V1Fields.clientVersion)
        & V2Fields.gameDataVersion .~ (source ^. V1Fields.gameDataVersion)
        & V2Fields.progress .~ (source ^. V1Fields.progress)
        & V2Fields.deck .~ (source ^. V1Fields.deck)
        & V2Fields.inventory .~ (source ^. V1Fields.inventory)
        & V2Fields.equipment .~ (source ^. V1Fields.equipment)
        & V2Fields.roster .~ roster
        & V2Fields.collection .~ collection
        & V2Fields.wallet .~ (source ^. V1Fields.wallet)
        & V2Fields.mail .~ (source ^. V1Fields.mail)
        & V2Fields.missions .~ (source ^. V1Fields.missions)

characterRecord :: V2.CharacterOrigin -> V1.Character -> V2.CharacterRecord
characterRecord origin character =
  defMessage & V2Fields.character .~ character & V2Fields.origin .~ origin

validateV2 :: V2.Snapshot -> [Problem]
validateV2 snapshot =
  let roster = snapshot ^. V2Fields.roster
      records = toList (roster ^. V2Fields.characters)
      projectionProblems = fst (validateSnapshot (projectV1ForSharedValidation snapshot))
      characterIndices = map (^. V2Fields.character . V1Fields.inventoryIndex) records
      acquiredIndices =
        [ record ^. V2Fields.character . V1Fields.inventoryIndex
        | record <- records
        , record ^. V2Fields.origin == V2.CHARACTER_ORIGIN_ACQUIRED
        ]
      costumeIndices = map (^. V1Fields.inventoryIndex) (toList (roster ^. V2Fields.costumes))
      ownedCharacters = Set.fromList characterIndices
      collection = snapshot ^. V2Fields.collection
   in projectionProblems <> concat
        [ requireV2 (snapshot ^. V2Fields.formatVersion == 2) "snapshot.format_version" ["format_version"] "snapshot format version must be 2" []
        , requireV2 (snapshot ^. V2Fields.clientVersion == "2.34.13") "snapshot.client_version" ["client_version"] "unsupported client version" []
        , requireV2 (not (Text.null (snapshot ^. V2Fields.gameDataVersion))) "snapshot.game_data_version" ["game_data_version"] "game data version is required" []
        , duplicateV2 "roster.duplicate_character_index" ["roster", "characters"] characterIndices
        , duplicateV2 "roster.duplicate_costume_index" ["roster", "costumes"] costumeIndices
        , [ Problem "roster.unspecified_origin" Error ["roster", "characters"] "character origin must be explicit" [index]
          | record <- records
          , record ^. V2Fields.origin /= V2.CHARACTER_ORIGIN_BASE
              && record ^. V2Fields.origin /= V2.CHARACTER_ORIGIN_ACQUIRED
          , let index = record ^. V2Fields.character . V1Fields.inventoryIndex
          ]
        , nextIndexV2 "roster.next_acquired_character_index" ["roster", "next_acquired_character_index"]
            (roster ^. V2Fields.nextAcquiredCharacterIndex) acquiredIndices
        , nextIndexV2 "roster.next_costume_index" ["roster", "next_costume_index"]
            (roster ^. V2Fields.nextCostumeIndex) costumeIndices
        , [ Problem "roster.costume_unknown_character" Error ["roster", "costumes"]
              "costume owner must be in the unified roster" [costume ^. V1Fields.inventoryIndex, owner]
          | costume <- toList (roster ^. V2Fields.costumes)
          , let owner = costume ^. V1Fields.useChar
          , owner /= 0 && Set.notMember owner ownedCharacters
          ]
        , requireV2 (not (collection ^. V2Fields.previewLocked) || not (null (toList (collection ^. V2Fields.latestPreview))))
            "collection.locked_preview_missing" ["collection", "latest_preview"] "locked preview must retain its result" []
        ]

-- Reuse all V1 structural and unchanged-domain checks without making V2's
-- storage layout depend on the old split. This projection is validation-only.
projectV1ForSharedValidation :: V2.Snapshot -> V1.Snapshot
projectV1ForSharedValidation snapshot =
  let roster = snapshot ^. V2Fields.roster
      records = toList (roster ^. V2Fields.characters)
      baseCharacters =
        [ record ^. V2Fields.character
        | record <- records
        , record ^. V2Fields.origin == V2.CHARACTER_ORIGIN_BASE
        ]
      acquiredCharacters =
        [ record ^. V2Fields.character
        | record <- records
        , record ^. V2Fields.origin == V2.CHARACTER_ORIGIN_ACQUIRED
        ]
      ledger = snapshot ^. V2Fields.collection
      characters =
        defMessage
          & V1Fields.clientVersion .~ (snapshot ^. V2Fields.clientVersion)
          & V1Fields.characters .~ baseCharacters
      collection =
        defMessage
          & V1Fields.clientVersion .~ (snapshot ^. V2Fields.clientVersion)
          & V1Fields.nextCharacterIndex .~ (roster ^. V2Fields.nextAcquiredCharacterIndex)
          & V1Fields.nextCostumeIndex .~ (roster ^. V2Fields.nextCostumeIndex)
          & V1Fields.latestPreview .~ toList (ledger ^. V2Fields.latestPreview)
          & V1Fields.previewEventIndex .~ (ledger ^. V2Fields.previewEventIndex)
          & V1Fields.previewLocked .~ (ledger ^. V2Fields.previewLocked)
          & V1Fields.characters .~ acquiredCharacters
          & V1Fields.costumes .~ toList (roster ^. V2Fields.costumes)
          & V1Fields.baseCostumeLevels .~ toList (ledger ^. V2Fields.baseCostumeLevels)
          & V1Fields.gachaSelections .~ toList (ledger ^. V2Fields.gachaSelections)
          & V1Fields.stepUpProgress .~ toList (ledger ^. V2Fields.stepUpProgress)
          & V1Fields.gachaUsers .~ toList (ledger ^. V2Fields.gachaUsers)
          & V1Fields.gachaFixed .~ toList (ledger ^. V2Fields.gachaFixed)
          & V1Fields.gachaApplied .~ toList (ledger ^. V2Fields.gachaApplied)
          & V1Fields.gachaPointExchanges .~ toList (ledger ^. V2Fields.gachaPointExchanges)
          & V1Fields.gachaCountCorrected .~ (ledger ^. V2Fields.gachaCountCorrected)
          & V1Fields.grants .~ toList (ledger ^. V2Fields.grants)
   in defMessage
        & V1Fields.formatVersion .~ 1
        & V1Fields.clientVersion .~ (snapshot ^. V2Fields.clientVersion)
        & V1Fields.gameDataVersion .~ (snapshot ^. V2Fields.gameDataVersion)
        & V1Fields.progress .~ (snapshot ^. V2Fields.progress)
        & V1Fields.deck .~ (snapshot ^. V2Fields.deck)
        & V1Fields.inventory .~ (snapshot ^. V2Fields.inventory)
        & V1Fields.equipment .~ (snapshot ^. V2Fields.equipment)
        & V1Fields.characters .~ characters
        & V1Fields.collection .~ collection
        & V1Fields.wallet .~ (snapshot ^. V2Fields.wallet)
        & V1Fields.mail .~ (snapshot ^. V2Fields.mail)
        & V1Fields.missions .~ (snapshot ^. V2Fields.missions)

requireV2 :: Bool -> Text -> [Text] -> Text -> [Word64] -> [Problem]
requireV2 condition code path message related =
  [Problem code Error path message related | not condition]

duplicates :: Ord a => [a] -> [a]
duplicates = map head . filter ((> 1) . length) . group . sort

duplicateV2 :: Text -> [Text] -> [Word64] -> [Problem]
duplicateV2 code path values =
  [Problem code Error path "duplicate inventory identity" [value] | value <- duplicates values]

nextIndexV2 :: Text -> [Text] -> Word64 -> [Word64] -> [Problem]
nextIndexV2 code path nextIndex existing =
  requireV2 (null existing || nextIndex > maximum existing) code path "next index must be greater than every existing index" [nextIndex]
