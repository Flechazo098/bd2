namespace BD2.GameNames;

/// <summary>Specifies the kind of game member whose readable name should be resolved.</summary>
public enum GameMemberKind
{
    /// <summary>A method, including a coroutine entry point.</summary>
    Method,
    /// <summary>A field.</summary>
    Field,
    /// <summary>A property.</summary>
    Property,
    /// <summary>An event.</summary>
    Event
}
