using System;
using System.Collections.Generic;
using System.Linq;
using System.Reflection;
using System.Reflection.Emit;
using BD2.GameNames;
using BepInEx.Logging;
using HarmonyLib;

namespace Bd2LocalIdentity;

internal static class FieldPartyIndices
{
    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        Type closure = Game.FindType("FieldCharSettingPopupUI+<>c__DisplayClass37_0");
        MethodInfo select = closure?.GetGameMethod("<SetSelectedChar>b__0",
            BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)
            ?? throw new MissingMethodException("Field party selection method is unavailable");
        harmony.Patch(select, transpiler: new HarmonyMethod(typeof(FieldPartyIndices), nameof(PreserveInstanceIndices)));
        logger.LogInfo("Field party selection preserves 64-bit character and costume indices");
    }

    private static List<CodeInstruction> PreserveInstanceIndices(IEnumerable<CodeInstruction> instructions)
    {
        var code = instructions.ToList();
        const BindingFlags properties = BindingFlags.Instance | BindingFlags.Public;
        MethodInfo character = typeof(FieldTrainCharData).GetGameProperty("CharInvenIndex", properties)?.SetMethod;
        MethodInfo costume = typeof(FieldTrainCharData).GetGameProperty("CostumeInvenIndex", properties)?.SetMethod;
        if (character == null || costume == null)
            throw new MissingMemberException("Field party instance index properties are unavailable");
        int characters = 0, costumes = 0;
        for (int i = 1; i + 2 < code.Count; i++)
        {
            if ((code[i - 1].opcode != OpCodes.Call && code[i - 1].opcode != OpCodes.Callvirt) ||
                code[i - 1].operand is not MethodInfo { ReturnType: var sourceType } || sourceType != typeof(long) ||
                code[i].opcode != OpCodes.Conv_I4 || code[i + 1].opcode != OpCodes.Conv_I8 ||
                (code[i + 2].opcode != OpCodes.Call && code[i + 2].opcode != OpCodes.Callvirt) ||
                code[i + 2].operand is not MethodInfo setter || setter != character && setter != costume) continue;
            // Keep the long getter result on the stack. Nops retain branch labels
            // and exception boundaries attached to the native conversion pair.
            code[i].opcode = OpCodes.Nop;
            code[i + 1].opcode = OpCodes.Nop;
            if (setter == character) characters++;
            else costumes++;
        }
        if (characters != 1 || costumes != 1)
            throw new InvalidOperationException("Field party index conversions differ from the supported client");
        return code;
    }
}
