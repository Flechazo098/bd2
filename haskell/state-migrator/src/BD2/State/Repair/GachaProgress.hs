{-# LANGUAGE OverloadedStrings #-}

module BD2.State.Repair.GachaProgress
  ( GachaGroupFact (..)
  , CostumeGradeFact (..)
  , repairGachaProgress
  ) where

import BD2.State.Domain
import Data.Foldable (toList)
import Data.List (find, sortOn)
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
import Text.Read (readMaybe)

data GachaGroupFact = GachaGroupFact
  { factGachaId :: Word64
  , factGroupId :: Word64
  , factFixedId :: Word64
  , factPointCount :: Word64
  }
  deriving stock (Eq, Show)

data CostumeGradeFact = CostumeGradeFact
  { gradeCostumeId :: Word64
  , costumeGrade :: Word64
  }
  deriving stock (Eq, Show)

data Draw = Draw
  { drawIdentity :: Text
  , drawSession :: Text
  , drawSequence :: Word64
  , drawFact :: GachaGroupFact
  , drawGrant :: V1.CollectionGrant
  }

repairGachaProgress :: [GachaGroupFact] -> [CostumeGradeFact] -> V2.Snapshot -> Either [Problem] V2.Snapshot
repairGachaProgress groups grades snapshot = do
  groupMap <- uniqueMap "repair.gacha_duplicate_id" factGachaId groups
  gradeMap <- uniqueMap "repair.gacha_duplicate_costume" gradeCostumeId grades
  let ledger = snapshot ^. V2Fields.collection
      users = toList (ledger ^. V2Fields.gachaUsers)
      fixed = toList (ledger ^. V2Fields.gachaFixed)
      applied = Set.fromList (toList (ledger ^. V2Fields.gachaApplied))
      grants = toList (ledger ^. V2Fields.grants)
      initialized = not (null users) || not (null fixed) || not (Set.null applied)
  if initialized
    then repairCorrectedCounts groupMap snapshot
    else replayLegacy groupMap gradeMap grants snapshot

repairCorrectedCounts :: Map.Map Word64 GachaGroupFact -> V2.Snapshot -> Either [Problem] V2.Snapshot
repairCorrectedCounts groupMap snapshot
  | snapshot ^. V2Fields.collection . V2Fields.gachaCountCorrected = Right snapshot
  | otherwise = do
      let ledger = snapshot ^. V2Fields.collection
          applied = Set.fromList (toList (ledger ^. V2Fields.gachaApplied))
          grants = toList (ledger ^. V2Fields.grants)
          countGrant accumulated grant = case parseIdentity groupMap (grant ^. V1Fields.identity) of
            Just (_, _, _, fact)
              | Set.member (grant ^. V1Fields.identity) applied ->
                  Map.insertWith (+) (factGroupId fact) (fromIntegral (length (toList (grant ^. V1Fields.viewCostumeIds)))) accumulated
            _ -> accumulated
          counts = foldl countGrant Map.empty grants
          users = toList (ledger ^. V2Fields.gachaUsers)
          repairedUsers = foldl setCount users (Map.toList counts)
          setCount current (groupId, count) = upsertUser groupId (\user -> user & V1Fields.totalBuyCount .~ count) current
      Right $ snapshot
        & V2Fields.collection . V2Fields.gachaUsers .~ repairedUsers
        & V2Fields.collection . V2Fields.gachaCountCorrected .~ True

replayLegacy :: Map.Map Word64 GachaGroupFact -> Map.Map Word64 CostumeGradeFact -> [V1.CollectionGrant] -> V2.Snapshot -> Either [Problem] V2.Snapshot
replayLegacy groupMap gradeMap grants snapshot = do
  let draws = sortOn (\draw -> (sessionOrder (drawSession draw), drawSequence draw))
        [ Draw identity session sequenceNumber fact grant
        | grant <- grants
        , let identity = grant ^. V1Fields.identity
        , Just (_, session, sequenceNumber, fact) <- [parseIdentity groupMap identity]
        ]
  if null draws
    then Right snapshot
    else do
      (users, fixed, applied) <- foldl step (Right ([], [], [])) draws
      Right $ snapshot
        & V2Fields.collection . V2Fields.gachaUsers .~ users
        & V2Fields.collection . V2Fields.gachaFixed .~ fixed
        & V2Fields.collection . V2Fields.gachaApplied .~ applied
        & V2Fields.collection . V2Fields.gachaCountCorrected .~ True
  where
    step accumulated draw = do
      (users, fixed, applied) <- accumulated
      let fact = drawFact draw
          costumeIds = toList (drawGrant draw ^. V1Fields.viewCostumeIds)
          count = fromIntegral (length costumeIds)
          addUser user = user
            & V1Fields.groupId .~ factGroupId fact
            & V1Fields.totalBuyCount .~ (user ^. V1Fields.totalBuyCount) + count
            & V1Fields.point .~ (user ^. V1Fields.point) + factPointCount fact * count
          nextUsers = upsertUser (factGroupId fact) addUser users
      nextFixed <- if factFixedId fact == 0 then Right fixed else foldl (applyGrade (factFixedId fact)) (Right fixed) costumeIds
      Right (nextUsers, nextFixed, applied <> [drawIdentity draw])
    applyGrade fixedId current costumeId = do
      states <- current
      grade <- case Map.lookup costumeId gradeMap of
        Nothing -> Left [Problem "repair.gacha_missing_grade" Error ["collection", "grants"] "gacha result has no costume grade fact" [costumeId]]
        Just value -> Right (costumeGrade value)
      let fourKey = Text.pack (show fixedId <> ":0")
          fiveKey = Text.pack (show fixedId <> ":1")
          updateFour state = state & V1Fields.fixedId .~ fixedId & V1Fields.type' .~ 0 & V1Fields.applySortId .~ (-1)
            & V1Fields.count .~ case grade of 5 -> 0; 4 -> 0; _ -> state ^. V1Fields.count + 1
          updateFive state = state & V1Fields.fixedId .~ fixedId & V1Fields.type' .~ 1 & V1Fields.applySortId .~ (-1)
            & V1Fields.count .~ case grade of 5 -> 0; _ -> state ^. V1Fields.count + 1
      Right (upsertFixed fiveKey updateFive (upsertFixed fourKey updateFour states))

sessionOrder :: Text -> (Int, Text)
sessionOrder session
  | Text.null session = (0, "")
  | otherwise = (1, session)

parseIdentity :: Map.Map Word64 GachaGroupFact -> Text -> Maybe (Word64, Text, Word64, GachaGroupFact)
parseIdentity groups identity = do
  let parts = Text.splitOn ":" identity
  if length parts < 4 || head parts /= "regular-gacha" then Nothing else pure ()
  gachaId <- readWord (parts !! 1)
  fact <- Map.lookup gachaId groups
  sequenceNumber <- readWord (last parts)
  let session = if length parts >= 6 && parts !! 2 == "session" && parts !! 4 == "seq" then parts !! 3 else ""
  pure (gachaId, session, sequenceNumber, fact)

readWord :: Text -> Maybe Word64
readWord = readMaybe . Text.unpack

upsertUser :: Word64 -> (V1.GachaUser -> V1.GachaUser) -> [V1.NamedGachaUser] -> [V1.NamedGachaUser]
upsertUser groupId update entries =
  let key = Text.pack (show groupId)
   in case find ((== key) . (^. V1Fields.identity)) entries of
        Nothing -> entries <> [defMessage & V1Fields.identity .~ key & V1Fields.user .~ update defMessage]
        Just _ -> map (\entry -> if entry ^. V1Fields.identity == key then entry & V1Fields.user .~ update (entry ^. V1Fields.user) else entry) entries

upsertFixed :: Text -> (V1.GachaFixed -> V1.GachaFixed) -> [V1.NamedGachaFixed] -> [V1.NamedGachaFixed]
upsertFixed key update entries =
  case find ((== key) . (^. V1Fields.identity)) entries of
    Nothing -> entries <> [defMessage & V1Fields.identity .~ key & V1Fields.fixed .~ update defMessage]
    Just _ -> map (\entry -> if entry ^. V1Fields.identity == key then entry & V1Fields.fixed .~ update (entry ^. V1Fields.fixed) else entry) entries

uniqueMap :: Ord key => Text -> (value -> key) -> [value] -> Either [Problem] (Map.Map key value)
uniqueMap code keyOf = foldl insert (Right Map.empty)
  where
    insert accumulated value = do
      current <- accumulated
      let key = keyOf value
      if Map.member key current
        then Left [Problem code Error ["validation_context"] "duplicate validation fact" []]
        else Right (Map.insert key value current)
