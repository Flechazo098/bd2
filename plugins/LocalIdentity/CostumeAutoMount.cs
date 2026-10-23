using System;
using System.Collections.Generic;
using System.Linq;
using System.Reflection;
using System.Reflection.Emit;
using BD2.GameNames;
using BepInEx.Logging;
using HarmonyLib;
using Proto.Net;

namespace Bd2LocalIdentity;

internal static class CostumeAutoMount
{
    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        Type closure = Game.FindType("AutoMountManager+<>c__DisplayClass24_0");
        MethodInfo apply = closure?.GetGameMethod("<ProcessCostumeAppliedOptions>g__AfterCostumeUseBatchRequest|1",
            BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)
            ?? throw new MissingMethodException("Costume auto-mount batch method is unavailable");
        harmony.Patch(apply, transpiler: new HarmonyMethod(typeof(CostumeAutoMount), nameof(PreserveInstanceIndices)));
        logger.LogInfo("Costume auto-mount preserves 64-bit character and costume indices");
    }

    private static List<CodeInstruction> PreserveInstanceIndices(IEnumerable<CodeInstruction> instructions)
    {
        var code = instructions.ToList();
        MethodInfo character = typeof(FieldDeckDBInfo).GetProperty(nameof(FieldDeckDBInfo.CharInvenIndex))?.SetMethod;
        MethodInfo costume = typeof(FieldDeckDBInfo).GetProperty(nameof(FieldDeckDBInfo.CostumeInvenIndex))?.SetMethod;
        int repaired = 0;
        for (int i = 1; i + 2 < code.Count; i++)
        {
            if (code[i - 1].opcode != OpCodes.Ldfld || code[i - 1].operand is not FieldInfo { FieldType: var fieldType } ||
                fieldType != typeof(long) || code[i].opcode != OpCodes.Conv_I4 || code[i + 1].opcode != OpCodes.Conv_I8 ||
                code[i + 2].operand is not MethodInfo setter || setter != character && setter != costume) continue;
            // Native auto-mount narrows these two long indices before assigning the long protocol fields.
            code[i].opcode = OpCodes.Nop;
            code[i + 1].opcode = OpCodes.Nop;
            repaired++;
        }
        if (repaired != 2) throw new InvalidOperationException("Costume auto-mount index conversions differ from the supported client");
        return code;
    }
}
