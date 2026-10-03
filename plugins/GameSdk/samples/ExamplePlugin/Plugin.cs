using System;
using System.Reflection;
using BD2.GameNames;
using BepInEx;
using HarmonyLib;

namespace ExamplePlugin;

[BepInPlugin("example.readable-names", "Readable Names Example", "1.0.0")]
public sealed class Plugin : BaseUnityPlugin
{
    private Harmony harmony;

    private void Awake()
    {
        try
        {
            // Fail before installing any game patches when binary/table/plugin versions differ.
            Game.Validate(typeof(Plugin).Assembly, message => Logger.LogInfo(message));
            MethodInfo target = Game.Method<IntroUI>(ui => ui.SendMaintenanceInfo(false));
            harmony = new Harmony("example.readable-names");
            harmony.Patch(target, prefix: new HarmonyMethod(typeof(Plugin), nameof(BeforeMaintenance)));

            // Private members require the runtime string channel. The prefix expression above
            // and the Unity callback name below belong to separate channels.
            var awake = typeof(IntroUI).GetGameMethod("Awake",
                BindingFlags.Instance | BindingFlags.NonPublic, null, Type.EmptyTypes, null);
            Logger.LogInfo("Resolved IntroUI.Awake: " + (awake != null));
        }
        catch (Exception exception)
        {
            Logger.LogError("Readable Names Example initialization failed: " + exception);
        }
    }

    private static void BeforeMaintenance() { }
    private void OnDestroy() => harmony?.UnpatchSelf();
}
