{-# LANGUAGE OverloadedStrings #-}

module Main (main) where

import BD2.State.Domain
import BD2.State.Framing
import BD2.State.Migrate.V1ToV2
import BD2.State.Repair.Pipeline
import BD2.State.Validate
import qualified Data.ByteString as BS
import Data.ProtoLens (defMessage)
import Lens.Family2 ((&), (.~), (^.))
import qualified Proto.Bd2.State.Control.V1.Control as Control
import qualified Proto.Bd2.State.Control.V1.Control_Fields as ControlFields
import qualified Proto.Bd2.State.Control.V2.Control as ControlV2
import qualified Proto.Bd2.State.Control.V2.Control_Fields as ControlV2Fields
import System.Environment (getArgs)
import System.Exit (exitFailure)
import System.IO (hPutStrLn, stderr)

main :: IO ()
main = do
  args <- getArgs
  input <- BS.getContents
  case args of
    [] -> runCheck input
    ["check"] -> runCheck input
    ["migrate-v1-v2"] -> runMigrateV1ToV2 input
    ["repair"] -> runRepair input
    _ -> hPutStrLn stderr "usage: bd2-state [check|migrate-v1-v2|repair]" >> exitFailure

runCheck :: BS.ByteString -> IO ()
runCheck input =
  case (decodeDelimited input :: Either String Control.CheckRequest) of
    Left err -> hPutStrLn stderr ("bd2-state: " <> err) >> exitFailure
    Right request -> do
      let (problems, _) = validateRequest request
          response :: Control.CheckResponse
          response =
            defMessage
              & ControlFields.bridgeApiVersion .~ 1
              & ControlFields.sourceSha256 .~ (request ^. ControlFields.sourceSha256)
              & ControlFields.violations .~ map problemToViolation problems
      BS.putStr (encodeDelimited response)

runRepair :: BS.ByteString -> IO ()
runRepair input =
  case (decodeDelimited input :: Either String ControlV2.RepairRequest) of
    Left err -> hPutStrLn stderr ("bd2-state: " <> err) >> exitFailure
    Right request -> do
      let requestProblems =
            [ Problem "bridge.version" Error ["bridge_api_version"] "bridge API version must be 2" []
            | request ^. ControlV2Fields.bridgeApiVersion /= 2
            ]
              <> [ Problem "bridge.source_hash" Error ["source_sha256"] "source SHA-256 must contain 32 bytes" []
                 | BS.length (request ^. ControlV2Fields.sourceSha256) /= 32
                 ]
          outcome = if null requestProblems then repairV1ToCurrent request else Left requestProblems
          responseBase :: ControlV2.RepairResponse
          responseBase = defMessage
            & ControlV2Fields.bridgeApiVersion .~ 2
            & ControlV2Fields.sourceSha256 .~ (request ^. ControlV2Fields.sourceSha256)
          response = case outcome of
            Left problems -> responseBase & ControlV2Fields.violations .~ map problemToViolation problems
            Right (target, changed) -> responseBase
              & ControlV2Fields.repaired .~ True
              & ControlV2Fields.changed .~ changed
              & ControlV2Fields.target .~ target
      BS.putStr (encodeDelimited response)

runMigrateV1ToV2 :: BS.ByteString -> IO ()
runMigrateV1ToV2 input =
  case (decodeDelimited input :: Either String ControlV2.MigrateV1ToV2Request) of
    Left err -> hPutStrLn stderr ("bd2-state: " <> err) >> exitFailure
    Right request -> do
      let source = request ^. ControlV2Fields.source
          sourceProblems =
            [ Problem "bridge.version" Error ["bridge_api_version"] "bridge API version must be 2" []
            | request ^. ControlV2Fields.bridgeApiVersion /= 2
            ]
              <> [ Problem "bridge.source_hash" Error ["source_sha256"] "source SHA-256 must contain 32 bytes" []
                 | BS.length (request ^. ControlV2Fields.sourceSha256) /= 32
                 ]
          outcome = if null sourceProblems then migrateV1ToV2 source else Left sourceProblems
          responseBase :: ControlV2.MigrateV1ToV2Response
          responseBase =
            defMessage
              & ControlV2Fields.bridgeApiVersion .~ 2
              & ControlV2Fields.sourceSha256 .~ (request ^. ControlV2Fields.sourceSha256)
          response = case outcome of
            Left problems -> responseBase & ControlV2Fields.violations .~ map problemToViolation problems
            Right target -> responseBase & ControlV2Fields.migrated .~ True & ControlV2Fields.target .~ target
      BS.putStr (encodeDelimited response)

problemToViolation :: Problem -> Control.Violation
problemToViolation problem =
  defMessage
    & ControlFields.code .~ problemCode problem
    & ControlFields.severity .~ case problemSeverity problem of
        Warning -> Control.SEVERITY_WARNING
        Error -> Control.SEVERITY_ERROR
    & ControlFields.path .~ problemPath problem
    & ControlFields.message .~ problemMessage problem
    & ControlFields.relatedIds .~ problemRelatedIds problem
