using System;
using System.Collections;
using System.IO;
using System.Reflection;
using BD2.GameNames;
using UnityEngine;
using UnityEngine.Events;
using UnityEngine.UI;

using static BD2.GameNames.Game;
using static Bd2LoginUI.LoginRuntime;
using static Bd2LoginUI.LoginController;

namespace Bd2LoginUI;

// Owns login branding, provider layout and button listeners.
internal static class LoginPanel
{
    private const string SymbolResource = "Bd2LoginUI.Assets.Discord-Symbol.png";
    private const string WordmarkResource = "Bd2LoginUI.Assets.Discord-Wordmark.png";
    private static readonly Color DiscordBlurple = new Color32(88, 101, 242, 255);

    private static Sprite DiscordSymbol;
    private static Sprite DiscordWordmark;
    internal static void ConfigureLoginPanel(object introUI)
    {
        if (Authentication == null || Authentication.mode != "oauth")
        {
            return;
        }
        try
        {
            Component component = introUI as Component;
            Transform panel = component == null ? null : FindDescendant(component.transform, "SignInWithAccount");
            if (panel == null)
            {
                throw new MissingMemberException("IntroUI/SignInWithAccount was not found");
            }

            Button google = FindButton(panel, "Button - Google");
            Button discord = FindButton(panel, "Button - Facebook");
            if (discord == null)
            {
                discord = FindButton(panel, "Button - Discord");
            }
            if (google == null || discord == null)
            {
                throw new MissingMemberException("Google or Facebook/Discord login button was not found");
            }

            Image box = FindImage(discord.transform, "Image - Box");
            Image logo = FindImage(discord.transform, "Image - Logo");
            Image title = FindImage(discord.transform, "Image - Title");
            if (box == null || logo == null || title == null)
            {
                throw new MissingMemberException("Discord button images were not found");
            }

            SetActive(panel, "Button - Apple", false);
            SetActive(panel, "Button - Email", false);
            SetActive(panel, "Button - Mail", false);
            SetActive(panel, "Button - Guest", false);

            google.gameObject.SetActive(ProviderEnabled("google"));
            discord.gameObject.SetActive(ProviderEnabled("discord"));
            int providerCount = (google.gameObject.activeSelf ? 1 : 0) +
                (discord.gameObject.activeSelf ? 1 : 0);
            discord.transform.SetSiblingIndex(0);
            google.transform.SetSiblingIndex(1);
            discord.gameObject.name = "Button - Discord";

            ReplaceClick(google, introUI, "google");
            ReplaceClick(discord, introUI, "discord");
            ApplyDiscordBrand(box, logo, title);
            ConfigureProviderGrid(panel, providerCount);

            Canvas.ForceUpdateCanvases();
            if (panel is RectTransform panelRect)
            {
                LayoutRebuilder.ForceRebuildLayoutImmediate(panelRect);
            }
        }
        catch (Exception ex)
        {
            Log?.LogError("Could not configure login panel: " + ex);
        }
    }

    private static void ConfigureProviderGrid(Transform panel, int providerCount)
    {
        GridLayoutGroup grid = panel.GetComponentInChildren<GridLayoutGroup>(true);
        if (grid == null)
        {
            throw new MissingMemberException("Login provider grid was not found");
        }
        int columns = Math.Max(1, providerCount);
        grid.constraint = GridLayoutGroup.Constraint.FixedColumnCount;
        grid.constraintCount = columns;
        if (grid.transform is RectTransform gridRect)
        {
            float width = grid.padding.horizontal + grid.cellSize.x * columns +
                grid.spacing.x * Math.Max(0, columns - 1);
            UpdateBetterGridProfiles(grid, columns);
            UpdateBetterLocatorProfiles(grid, width);
            gridRect.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, width);
            LayoutRebuilder.ForceRebuildLayoutImmediate(gridRect);
        }
    }

    private static void UpdateBetterGridProfiles(GridLayoutGroup grid, int columns)
    {
        Type type = grid.GetType();
        if (type.FullName != "TheraBytes.BetterUi.BetterGridLayoutGroup")
        {
            return;
        }
        const BindingFlags flags = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
        UpdateBetterGridSettings(type.GetGameField("settingsFallback", flags)?.GetValue(grid), columns);
        object collection = type.GetGameField("customSettings", flags)?.GetValue(grid);
        IEnumerable items = collection?.GetType().GetGameProperty("Items", flags)?.GetValue(collection, null) as IEnumerable;
        if (items == null)
        {
            return;
        }
        foreach (object settings in items)
        {
            UpdateBetterGridSettings(settings, columns);
        }
    }

    private static void UpdateBetterGridSettings(object settings, int columns)
    {
        if (settings == null)
        {
            return;
        }
        Type type = settings.GetType();
        FieldInfo constraint = type.GetGameField("Constraint", BindingFlags.Instance | BindingFlags.Public);
        FieldInfo count = type.GetGameField("ConstraintCount", BindingFlags.Instance | BindingFlags.Public);
        if (constraint != null)
        {
            constraint.SetValue(settings, Enum.ToObject(constraint.FieldType, (int)GridLayoutGroup.Constraint.FixedColumnCount));
        }
        count?.SetValue(settings, columns);
    }

    private static void UpdateBetterLocatorProfiles(GridLayoutGroup grid, float width)
    {
        const BindingFlags flags = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
        foreach (Component component in grid.GetComponents<Component>())
        {
            Type type = component?.GetType();
            if (type?.FullName != "TheraBytes.BetterUi.BetterLocator")
            {
                continue;
            }
            UpdateBetterRectTransformData(type.GetGameField("transformFallback", flags)?.GetValue(component), width);
            object collection = type.GetGameField("transformConfigs", flags)?.GetValue(component);
            IEnumerable items = collection?.GetType().GetGameProperty("Items", flags)?.GetValue(collection, null) as IEnumerable;
            if (items == null)
            {
                continue;
            }
            foreach (object data in items)
            {
                UpdateBetterRectTransformData(data, width);
            }
        }
    }

    private static void UpdateBetterRectTransformData(object data, float width)
    {
        FieldInfo sizeField = data?.GetType().GetGameField("SizeDelta", BindingFlags.Instance | BindingFlags.Public);
        if (sizeField == null || sizeField.FieldType != typeof(Vector2))
        {
            return;
        }
        Vector2 size = (Vector2)sizeField.GetValue(data);
        size.x = width;
        sizeField.SetValue(data, size);
    }

    private static void ReplaceClick(Button button, object introUI, string provider)
    {
        // Assigning a fresh event removes both serialized persistent calls and
        // the listeners that IntroUI.Awake adds at runtime.
        button.onClick = new Button.ButtonClickedEvent();
        button.onClick.AddListener(new UnityAction(delegate { OpenLogin(introUI, provider); }));
        button.interactable = true;
    }

    private static void ApplyDiscordBrand(Image box, Image logo, Image title)
    {
        box.color = DiscordBlurple;
        logo.sprite = DiscordSymbol;
        logo.color = Color.white;
        logo.preserveAspect = true;
        title.sprite = DiscordWordmark;
        title.color = Color.white;
        title.preserveAspect = true;
        DisableSpriteLocalizer(logo.gameObject);
        DisableSpriteLocalizer(title.gameObject);
    }

    private static void DisableSpriteLocalizer(GameObject target)
    {
        foreach (Behaviour behaviour in target.GetComponents<Behaviour>())
        {
            if (behaviour is SpriteLocalizer)
            {
                behaviour.enabled = false;
            }
        }
    }

    private static Sprite LoadSprite(string resourceName, string name)
    {
        using Stream stream = Assembly.GetExecutingAssembly().GetManifestResourceStream(resourceName);
        if (stream == null)
        {
            throw new FileNotFoundException("Embedded login asset is missing", resourceName);
        }
        byte[] bytes = new byte[stream.Length];
        int offset = 0;
        while (offset < bytes.Length)
        {
            int read = stream.Read(bytes, offset, bytes.Length - offset);
            if (read == 0)
            {
                throw new EndOfStreamException("Unexpected end of embedded login asset " + resourceName);
            }
            offset += read;
        }
        Texture2D texture = new Texture2D(2, 2, TextureFormat.RGBA32, false, false)
        {
            name = name,
            filterMode = FilterMode.Bilinear,
            wrapMode = TextureWrapMode.Clamp
        };
        if (!ImageConversion.LoadImage(texture, bytes, true))
        {
            UnityEngine.Object.Destroy(texture);
            throw new InvalidDataException("Could not decode embedded login asset " + resourceName);
        }
        Sprite sprite = Sprite.Create(
            texture,
            new Rect(0f, 0f, texture.width, texture.height),
            new Vector2(0.5f, 0.5f),
            100f);
        sprite.name = name;
        return sprite;
    }

    private static Button FindButton(Transform root, string name)
    {
        Transform match = FindDescendant(root, name);
        return match == null ? null : match.GetComponent<Button>();
    }

    private static Image FindImage(Transform root, string name)
    {
        Transform match = FindDescendant(root, name);
        return match == null ? null : match.GetComponent<Image>();
    }

    internal static void SetActive(Transform root, string name, bool active)
    {
        Transform match = FindDescendant(root, name);
        if (match != null)
        {
            match.gameObject.SetActive(active);
        }
    }

    private static Transform FindDescendant(Transform root, string name)
    {
        if (root == null)
        {
            return null;
        }
        if (root.name == name)
        {
            return root;
        }
        for (int i = 0; i < root.childCount; i++)
        {
            Transform match = FindDescendant(root.GetChild(i), name);
            if (match != null)
            {
                return match;
            }
        }
        return null;
    }

    internal static void InitializeBranding()
    {
        DiscordSymbol = LoadSprite(SymbolResource, "BD2 Discord Symbol");
        DiscordWordmark = LoadSprite(WordmarkResource, "BD2 Discord Wordmark");
    }
}
