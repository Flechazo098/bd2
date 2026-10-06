#pragma warning disable IDE0005
using System.Collections.Generic;
#pragma warning restore IDE0005
using System.Runtime.Serialization;

// DataContractJsonSerializer populates these internal fields from the embedded table.
#pragma warning disable CS0649

#pragma warning disable IDE0130
namespace BD2.GameNames.Internal;
#pragma warning restore IDE0130

[DataContract]
internal sealed class NameTable
{
    [DataMember] public int schema_version = 1;
    [DataMember] public string game_version;
    [DataMember] public string assembly_name;
    [DataMember] public string assembly_mvid;
    [DataMember] public string assembly_sha256;
    [DataMember] public string mapping_sha256;
    [DataMember] public string generator_sha256;
    [DataMember] public List<TypeName> types = [];
    [DataMember] public List<MemberName> members = [];
}

[DataContract]
internal sealed class TypeName
{
    [DataMember] public int token;
    [DataMember] public string readable;
    [DataMember] public string original;
}

[DataContract]
internal sealed class MemberName
{
    [DataMember] public int token;
    [DataMember] public string kind;
    [DataMember] public string declaring_type;
    [DataMember] public string readable;
    [DataMember] public string original;
    [DataMember] public string signature;
    [DataMember] public List<ParameterName> parameters = [];
}

[DataContract]
internal sealed class ParameterName
{
    [DataMember] public int position;
    [DataMember] public string readable;
    [DataMember] public string original;
}
