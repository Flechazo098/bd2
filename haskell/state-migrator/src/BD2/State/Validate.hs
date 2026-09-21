{-# LANGUAGE OverloadedStrings #-}

module BD2.State.Validate
  ( validateRequest
  , validateSnapshot
  ) where

import BD2.State.Domain
import qualified Data.ByteString as BS
import Data.Foldable (toList)
import Data.List (group, sort)
import qualified Data.Set as Set
import Data.Text (Text)
import qualified Data.Text as Text
import Data.Word (Word32, Word64)
import Lens.Family2 ((^.))
import qualified Proto.Bd2.State.Control.V1.Control as Control
import qualified Proto.Bd2.State.Control.V1.Control_Fields as ControlFields
import qualified Proto.Bd2.State.V1.State as State
import qualified Proto.Bd2.State.V1.State_Fields as StateFields

validateRequest :: Control.CheckRequest -> ([Problem], Maybe ValidSnapshot)
validateRequest request =
  let requestProblems =
        require (request ^. ControlFields.bridgeApiVersion == 1) "bridge.version" ["bridge_api_version"] "bridge API version must be 1" []
          ++ require (BS.length (request ^. ControlFields.sourceSha256) == 32) "bridge.source_hash" ["source_sha256"] "source SHA-256 must contain 32 bytes" []
      (snapshotProblems, valid) = validateSnapshot (request ^. ControlFields.snapshot)
      allProblems = requestProblems <> snapshotProblems
   in (allProblems, if any ((== Error) . problemSeverity) allProblems then Nothing else valid)

validateSnapshot :: State.Snapshot -> ([Problem], Maybe ValidSnapshot)
validateSnapshot snapshot =
  let inventory = snapshot ^. StateFields.inventory
      equipment = snapshot ^. StateFields.equipment
      collection = snapshot ^. StateFields.collection
      progress = snapshot ^. StateFields.progress
      baseCharacters = toList (snapshot ^. StateFields.characters . StateFields.characters)
      acquiredCharacters = toList (collection ^. StateFields.characters)
      costumes = toList (collection ^. StateFields.costumes)
      itemIndices = map (^. StateFields.inventoryIndex) (toList (inventory ^. StateFields.items))
      equipmentIndices = map (^. StateFields.inventoryIndex) (toList (equipment ^. StateFields.equipment))
      characterIndices = map (^. StateFields.inventoryIndex) (baseCharacters <> acquiredCharacters)
      costumeIndices = map (^. StateFields.inventoryIndex) costumes
      ownedEquipment = Set.fromList equipmentIndices
      ownedCharacters = Set.fromList characterIndices
      questKeys = map questTuple (toList (progress ^. StateFields.quests))
      clearedKeys = map keyTuple (toList (progress ^. StateFields.clearedQuests))
      -- grant_items is a historical issuance ledger. Consuming a stack
      -- removes it from items but deliberately retains its grant marker.
      granted = Set.fromList (toList (inventory ^. StateFields.grantedIdentities))
      grantProblems = concatMap (validateGrant granted (inventory ^. StateFields.nextIndex)) (toList (inventory ^. StateFields.grantItems))
      problems = concat
        [ require (snapshot ^. StateFields.formatVersion == 1) "snapshot.format_version" ["format_version"] "snapshot format version must be 1" []
        , require (snapshot ^. StateFields.clientVersion == "2.34.13") "snapshot.client_version" ["client_version"] "unsupported client version" []
        , require (not (Text.null (snapshot ^. StateFields.gameDataVersion))) "snapshot.game_data_version" ["game_data_version"] "game data version is required" []
        , duplicateProblems "inventory.duplicate_index" ["inventory", "items"] itemIndices
        , duplicateProblems "equipment.duplicate_index" ["equipment", "equipment"] equipmentIndices
        , duplicateProblems "characters.duplicate_index" ["characters"] characterIndices
        , duplicateProblems "costumes.duplicate_index" ["collection", "costumes"] costumeIndices
        , nextIndexProblem "inventory.next_index" ["inventory", "next_index"] (inventory ^. StateFields.nextIndex) itemIndices
        , nextIndexProblem "equipment.next_index" ["equipment", "next_index"] (equipment ^. StateFields.nextIndex) equipmentIndices
        , nextIndexProblem "collection.next_character_index" ["collection", "next_character_index"] (collection ^. StateFields.nextCharacterIndex) characterIndices
        , nextIndexProblem "collection.next_costume_index" ["collection", "next_costume_index"] (collection ^. StateFields.nextCostumeIndex) costumeIndices
        , [ Problem "equipment.grant_missing_equipment" Error ["equipment", "grants", grant ^. StateFields.identity]
              "equipment grant must point to an owned equipment instance" [index]
          | grant <- toList (equipment ^. StateFields.grants)
          , let index = grant ^. StateFields.inventoryIndex
          , index == 0 || Set.notMember index ownedEquipment
          ]
        , [ Problem "equipment.unknown_user" Error ["equipment", "equipment"]
              "equipped character must be owned" [entry ^. StateFields.inventoryIndex, characterIndex]
          | entry <- toList (equipment ^. StateFields.equipment)
          , let characterIndex = entry ^. StateFields.useChar
          , characterIndex /= 0 && Set.notMember characterIndex ownedCharacters
          ]
        , keyProblems "progress.invalid_quest_key" ["progress", "quests"] questKeys
        , keyProblems "progress.invalid_cleared_key" ["progress", "cleared_quests"] clearedKeys
        , duplicateKeyProblems "progress.duplicate_quest_key" ["progress", "quests"] questKeys
        , duplicateKeyProblems "progress.duplicate_cleared_key" ["progress", "cleared_quests"] clearedKeys
        , grantProblems
        ]
   in ( problems
      , if any ((== Error) . problemSeverity) problems
          then Nothing
          else Just (ValidSnapshot snapshot baseCharacters acquiredCharacters costumes)
      )

questTuple :: State.QuestProgress -> (Word32, Word32)
questTuple quest = keyTuple (quest ^. StateFields.key)

keyTuple :: State.QuestKey -> (Word32, Word32)
keyTuple key = (key ^. StateFields.packId, key ^. StateFields.questId)

validateGrant :: Set.Set Text -> Word64 -> State.IndexedGrant -> [Problem]
validateGrant granted nextIndex grant =
  let identity = grant ^. StateFields.identity
      indices = toList (grant ^. StateFields.inventoryIndices)
      path = ["inventory", "grant_items", identity]
   in require (not (Text.null identity) && Set.member identity granted)
        "inventory.grant_without_marker" path "historical grant must have an idempotency marker" []
      <> [ Problem "inventory.grant_invalid_index" Error path "issued index must be nonzero and below next_index" [index]
         | index <- indices, index == 0 || index >= nextIndex
         ]
      <> duplicateProblems "inventory.grant_duplicate_index" path indices

require :: Bool -> Text -> [Text] -> Text -> [Word64] -> [Problem]
require condition code path message related =
  [Problem code Error path message related | not condition]

duplicates :: Ord a => [a] -> [a]
duplicates = map head . filter ((> 1) . length) . group . sort

duplicateProblems :: Text -> [Text] -> [Word64] -> [Problem]
duplicateProblems code path values =
  [Problem code Error path "duplicate inventory identity" [value] | value <- duplicates values]

nextIndexProblem :: Text -> [Text] -> Word64 -> [Word64] -> [Problem]
nextIndexProblem code path nextIndex existing =
  require (null existing || nextIndex > maximum existing) code path "next index must be greater than every existing index" [nextIndex]

keyProblems :: Text -> [Text] -> [(Word32, Word32)] -> [Problem]
keyProblems code path values =
  [ Problem code Error path "pack_id and quest_id must both be positive" [fromIntegral pack, fromIntegral quest]
  | (pack, quest) <- values
  , pack == 0 || quest == 0
  ]

duplicateKeyProblems :: Text -> [Text] -> [(Word32, Word32)] -> [Problem]
duplicateKeyProblems code path values =
  [ Problem code Error path "duplicate pack/quest pair" [fromIntegral pack, fromIntegral quest]
  | (pack, quest) <- duplicates values
  ]
