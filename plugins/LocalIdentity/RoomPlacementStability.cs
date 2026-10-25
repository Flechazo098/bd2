using System;
using System.Collections.Generic;
using System.Reflection;
using System.Reflection.Emit;
using BD2.GameNames;
using BepInEx.Logging;
using HarmonyLib;

namespace Bd2LocalIdentity;

internal static class RoomPlacementStability
{
    private static MethodInfo IsHideGetter;

    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        Type stateMachine = Game.FindType("MyRoom/<SetupBoardSkinObjectAsync>d__50")
            ?? throw new MissingMemberException("MyRoom board-skin setup state machine is unavailable");
        MethodInfo moveNext = stateMachine.GetMethod("MoveNext", BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)
            ?? throw new MissingMethodException("MyRoom board-skin setup MoveNext is unavailable");
        Type boardSkin = Game.FindType("MyRoomObjectBoardSkin")
            ?? throw new MissingMemberException("MyRoomObjectBoardSkin is unavailable");
        IsHideGetter = boardSkin.GetGameProperty("IsHide", BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)?.GetMethod
            ?? throw new MissingMethodException("MyRoomObjectBoardSkin.IsHide is unavailable");
        harmony.Patch(moveNext, transpiler: new HarmonyMethod(typeof(RoomPlacementStability), nameof(NullSafeBoardSkinVisibility)));
        logger.LogInfo("MyRoom placement tolerates an empty wall before the first board skin");
    }

    private static bool SafeIsHidden(object boardSkin)
    {
        return boardSkin != null && (bool)IsHideGetter.Invoke(boardSkin, null);
    }

    private static IEnumerable<CodeInstruction> NullSafeBoardSkinVisibility(IEnumerable<CodeInstruction> instructions)
    {
        MethodInfo safeGetter = AccessTools.Method(typeof(RoomPlacementStability), nameof(SafeIsHidden));
        int replaced = 0;
        foreach (CodeInstruction instruction in instructions)
        {
            if (instruction.Calls(IsHideGetter))
            {
                instruction.opcode = OpCodes.Call;
                instruction.operand = safeGetter;
                replaced++;
            }
            yield return instruction;
        }
        if (replaced != 1)
            throw new InvalidOperationException($"MyRoom board-skin visibility shape changed; replaced {replaced} getter(s)");
    }
}
