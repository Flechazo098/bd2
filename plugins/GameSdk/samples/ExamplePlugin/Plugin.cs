using System;
using System.Reflection;
using BD2.GameNames;
using BepInEx;
using HarmonyLib;
using TMPro;

namespace ExamplePlugin;

[BepInPlugin("example.plugin", "example plugin", "1.0.0")]
public sealed class Plugin : BaseUnityPlugin
{
    private const string Greeting = "\nHello! Brown Dust Ⅱ";
    private static FieldInfo versionTextField;

    private void Awake()
    {
        Game.Validate(typeof(Plugin).Assembly);
        MethodInfo target = Game.Method<IntroUI>(ui => ui.SetVersionText());
        versionTextField = typeof(IntroUI).GetGameField("_textVersion", BindingFlags.Instance | BindingFlags.NonPublic)
            ?? throw new MissingFieldException("IntroUI._textVersion");
        var harmony = new Harmony("example.plugin");
        harmony.Patch(target, postfix: new HarmonyMethod(typeof(Plugin), nameof(AfterSetVersionText)));
    }

    private static void AfterSetVersionText(IntroUI __instance)
    {
        if (versionTextField.GetValue(__instance) is not TMP_Text versionText) return;
        string current = versionText.text ?? string.Empty;
        if (current.EndsWith(Greeting, StringComparison.Ordinal)) return;
        versionText.text = current + Greeting;
    }
}
