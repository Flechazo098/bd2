using System;
using System.Collections;
using System.Diagnostics;
using System.Globalization;
using System.Reflection;
using System.Text;
using BD2.GameNames;
using HarmonyLib;
using UnityEngine;
using static Bd2LoginUI.LoginRuntime;

namespace Bd2LoginUI;

internal static class FieldPositionDiagnostics
{
    private const BindingFlags All = BindingFlags.Instance | BindingFlags.Static | BindingFlags.Public | BindingFlags.NonPublic;
    private static string LastTarget = "none";
    private static float LastTargetTime, LastRecoveryTime = -100;
    private static int RecoveryCount;

    internal static void Install(Harmony harmony)
    {
        Bind(harmony, "SetCharPosition", nameof(PositionPrefix), 1);
        Bind(harmony, "MoveSafeMapAtInvalidPosition", nameof(RecoveryPrefix), 0);
        try
        {
            MethodInfo setter = typeof(GameFieldManager).GetGameProperty("CurrentMapInfo", All)?.GetSetMethod(true);
            if (setter == null) throw new MissingMethodException();
            harmony.Patch(setter, prefix: new HarmonyMethod(typeof(FieldPositionDiagnostics), nameof(MapPrefix)));
            Type scene = typeof(SceneManager);
            MethodInfo load = scene?.GetGameMethod("ClearAndLoadMapScene", All);
            if (load == null || load.GetParameters().Length != 3) throw new MissingMethodException();
            harmony.Patch(load, prefix: new HarmonyMethod(typeof(FieldPositionDiagnostics), nameof(ScenePrefix)));
            Log?.LogInfo("Field transition diagnostics installed: CurrentMapInfo setter, ClearAndLoadMapScene");
        }
        catch (Exception error) { Log?.LogWarning("Field transition diagnostic binding unavailable: exception=" + error.GetType().Name); }
    }

    private static void Bind(Harmony harmony, string name, string prefix, int parameters)
    {
        try
        {
            MethodInfo method = typeof(GameFieldManager).GetGameMethod(name, All);
            if (method == null || method.GetParameters().Length != parameters) throw new MissingMethodException();
            harmony.Patch(method, prefix: new HarmonyMethod(typeof(FieldPositionDiagnostics), prefix));
            Log?.LogInfo("Field position diagnostic installed: target=" + name);
        }
        catch (Exception error) { Log?.LogWarning("Field position diagnostic binding unavailable: target=" + name + " exception=" + error.GetType().Name); }
    }

    private static object Read(object target, string name)
    {
        if (target == null) return null;
        Type type = target.GetType();
        return type.GetGameProperty(name, All)?.GetValue(target) ?? type.GetGameField(name, All)?.GetValue(target);
    }

    private static string Format(object value) => value is Vector3 point
        ? string.Format(CultureInfo.InvariantCulture, "({0:R},{1:R},{2:R})", point.x, point.y, point.z)
        : Convert.ToString(value, CultureInfo.InvariantCulture) ?? "unavailable";

    private static string ActiveScene()
    {
        try
        {
            return UnityEngine.SceneManagement.SceneManager.GetActiveScene().name;
        }
        catch { return "unavailable"; }
    }

    private static void PositionPrefix(object __0)
    {
        try
        {
            object mapId = Read(__0, "MapId");
            if (mapId == null || Convert.ToInt32(mapId, CultureInfo.InvariantCulture) == 0) return;
            LastTarget = "map=" + Format(mapId) + " xyz=" + Format(Read(__0, "PlayerPosition"));
            LastTargetTime = Time.realtimeSinceStartup;
            Log?.LogInfo("Field position target: " + LastTarget + " active_scene=" + ActiveScene() + " callers=" + Callers());
        }
        catch { }
    }

    private static string Callers()
    {
        var callers = new StringBuilder();
        foreach (StackFrame frame in new StackTrace(false).GetFrames() ?? Array.Empty<StackFrame>())
        {
            MethodBase method = frame.GetMethod();
            if (method?.DeclaringType == null || method.DeclaringType == typeof(FieldPositionDiagnostics)) continue;
            if (callers.Length > 0) callers.Append(" <- ");
            callers.Append(method.DeclaringType.FullName).Append('.').Append(method.Name);
            if (callers.Length >= 1600) break;
        }
        return callers.ToString();
    }

    private static void MapPrefix(object __instance, object __0)
    {
        try { Log?.LogInfo("Field map changed: previous=" + Format(Read(Read(__instance, "CurrentMapInfo"), "Id")) +
            " target=" + Format(Read(__0, "Id")) + " scene=" + Format(Read(__0, "MapScenePath")) + " active_scene=" + ActiveScene() + " callers=" + Callers()); }
        catch { }
    }

    private static void ScenePrefix(object __0, bool __1)
    {
        try { Log?.LogInfo("Field scene requested: pack=" + Format(Read(__0, "PackId")) +
            " map=" + Format(Read(__0, "Id")) + " scene=" + Format(Read(__0, "MapScenePath")) +
            " force=" + __1 + " active_scene=" + ActiveScene() + " callers=" + Callers()); }
        catch { }
    }

    private static void RecoveryPrefix(object __instance)
    {
        try
        {
            float now = Time.realtimeSinceStartup;
            RecoveryCount++;
            if (now - LastRecoveryTime < 5) return;
            LastRecoveryTime = now;
            object map = Read(__instance, "CurrentMapInfo");
            Component player = Read(__instance, "PlayerController") as Component;
            Component character = player == null ? null : player.GetComponent("CharacterController");
            object move = Read(player, "PlayerMoveController");
            object agent = Read(move, "Navagent");
            var callers = new StringBuilder();
            foreach (StackFrame frame in new StackTrace(false).GetFrames() ?? Array.Empty<StackFrame>())
            {
                MethodBase method = frame.GetMethod();
                if (method?.DeclaringType == null || method.DeclaringType == typeof(FieldPositionDiagnostics)) continue;
                if (callers.Length > 0) callers.Append(" <- ");
                callers.Append(method.DeclaringType.FullName).Append('.').Append(method.Name);
                if (callers.Length >= 1600) break;
            }
            object controller = Read(__instance, "FieldMapObjectController");
            bool hasController = controller is UnityEngine.Object unity ? unity != null : controller != null;
            Log?.LogWarning("Field position recovery: count=" + RecoveryCount +
                " pack=" + Format(Read(map, "PackId")) + " map=" + Format(Read(map, "Id")) +
                " player_xyz=" + (player == null ? "unavailable" : Format(player.transform.position)) +
                " scene_loaded=" + Format(Read(__instance, "IsLoadedField")) +
                " active_scene=" + ActiveScene() +
                " grounded=" + Format(Read(character, "isGrounded")) +
                " collision_flags=" + Format(Read(character, "collisionFlags")) +
                " move_type=" + Format(Read(move, "MoveType")) +
                " nav_on_mesh=" + Format(Read(agent, "isOnNavMesh")) +
                " nav_area_mask=" + Format(Read(agent, "areaMask")) +
                " encount_count=" + Format((Read(__instance, "FieldEncountPointList") as ICollection)?.Count) +
                " map_controller=" + hasController + " last_target={" + LastTarget + "} target_age_s=" +
                (now - LastTargetTime).ToString("F3", CultureInfo.InvariantCulture) + " callers=" + callers);
        }
        catch (Exception error) { Log?.LogWarning("Field position diagnostic unavailable: exception=" + error.GetType().Name); }
    }
}
