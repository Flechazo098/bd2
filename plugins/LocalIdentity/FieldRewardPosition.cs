using System;
using System.Collections;
using System.Collections.Generic;
using BD2.GameNames;
using BepInEx.Logging;
using gamfs;
using HarmonyLib;
using Proto.Net;
using UnityEngine.SceneManagement;

namespace Bd2LocalIdentity;

internal static class FieldRewardPosition
{
    private static readonly HashSet<FieldRewardObjectController> Pending = [];
    private static ManualLogSource Log;

    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        Log = logger;
        harmony.Patch(Game.Method(() => FieldPacket.SendFieldObjectRewardRequest(null, true)),
            prefix: new HarmonyMethod(typeof(FieldRewardPosition), nameof(SaveBeforeReward)));
    }

    private static bool SaveBeforeReward(FieldRewardObjectController __0, bool __1)
    {
        if (__0 == null || MiniGameHubSupporter.IsPlayEventHubOrMiniEventHub() ||
            (FieldRewardObjectController.EFieldRewardObjectGroupType)__0.FieldRewardObjectGroupDTO.Type ==
            FieldRewardObjectController.EFieldRewardObjectGroupType.LostCoin)
        {
            return true;
        }
        if (Pending.Contains(__0))
        {
            return false;
        }
        GameFieldManager field = GameFieldManager.Instance;
        if (UsesDedicatedPosition(field))
        {
            return true;
        }
        int packId = SG.Pack?.PlayingPackID ?? 0;
        Scene scene = __0.gameObject.scene;
        if (IsReady(field, packId, scene))
        {
            FieldPacket.SendSaveUserPositionRequest(packId, false, true);
            return true;
        }
        if (field == null || packId <= 0 || !scene.IsValid() || !scene.isLoaded)
        {
            Log.LogWarning("Field reward skipped without a loaded field: pack=" + packId);
            return false;
        }
        Pending.Add(__0);
        field.StartCoroutine(WaitForPosition(field, __0, __1, packId, scene));
        return false;
    }

    private static bool UsesDedicatedPosition(GameFieldManager field)
    {
        var packType = (Define_PackType)(SG.Pack?.EnterPackDTO?.PackType ?? -1);
        return packType is Define_PackType.PackHidden or Define_PackType.PackIb ||
            (packType == Define_PackType.PackSkyWay && field?.CurrentMapInfo?.MapType == 1) ||
            (packType == Define_PackType.PackEvilCastle && EvilCastleManager.Instance != null &&
                EvilCastleInfo.IsEvilCastleBType(EvilCastleManager.Instance.CurrentEvilCastleType));
    }

    private static bool IsReady(GameFieldManager field, int packId, Scene scene)
    {
        return field != null && field.PlayerController != null && field.IsLoadedField &&
            !field.IsMoveMapProcessing && field.CurrentMapInfo != null &&
            packId > 0 && field.CurrentMapInfo.PackId == packId && SG.Net != null &&
            scene.IsValid() && scene.isLoaded && scene == UnityEngine.SceneManagement.SceneManager.GetActiveScene() &&
            string.Equals(field.CurrentMapScenePath, scene.name, StringComparison.Ordinal);
    }

    private static IEnumerator WaitForPosition(GameFieldManager field,
        FieldRewardObjectController reward, bool showIndicator, int packId, Scene scene)
    {
        try
        {
            // The caller finishes its interaction state after the patched method returns.
            yield return null;
            while (field != null && reward != null && scene.isLoaded &&
                GameFieldManager.Instance == field && SG.Pack != null &&
                SG.Pack.PlayingPackID == packId && reward.gameObject.scene == scene)
            {
                if (IsReady(field, packId, scene))
                {
                    Pending.Remove(reward);
                    FieldPacket.SendFieldObjectRewardRequest(reward, showIndicator);
                    yield break;
                }
                yield return null;
            }
            Log.LogWarning("Deferred field reward cancelled after its field changed: pack=" + packId);
        }
        finally
        {
            Pending.Remove(reward);
        }
    }
}
