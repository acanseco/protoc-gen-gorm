package pluginv2

import (
	"fmt"
	"os"
	"strings"

	gorm "github.com/infobloxopen/protoc-gen-gorm/options"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

var (
	gormImport         = "github.com/jinzhu/gorm"
	tkgormImport       = "github.com/infobloxopen/atlas-app-toolkit/gorm"
	uuidImport         = "github.com/satori/go.uuid"
	authImport         = "github.com/infobloxopen/atlas-app-toolkit/auth"
	gormpqImport       = "github.com/jinzhu/gorm/dialects/postgres"
	gtypesImport       = "github.com/infobloxopen/protoc-gen-gorm/types"
	ptypesImport       = "github.com/golang/protobuf/ptypes"
	wktImport          = "github.com/golang/protobuf/ptypes/wrappers"
	resourceImport     = "github.com/infobloxopen/atlas-app-toolkit/gorm/resource"
	fmImport           = "google.golang.org/genproto/protobuf/field_mask"
	queryImport        = "github.com/infobloxopen/atlas-app-toolkit/query"
	ocTraceImport      = "go.opencensus.io/trace"
	gatewayImport      = "github.com/infobloxopen/atlas-app-toolkit/gateway"
	pqImport           = "github.com/lib/pq"
	gerrorsImport      = "github.com/infobloxopen/protoc-gen-gorm/errors"
	timestampImport    = "github.com/golang/protobuf/ptypes/timestamp"
	stdFmtImport       = "fmt"
	stdCtxImport       = "context"
	stdStringsImport   = "strings"
	stdTimeImport      = "time"
	encodingJsonImport = "encoding/json"
)

var builtinTypes = map[string]struct{}{
	"bool":    {},
	"int":     {},
	"int8":    {},
	"int16":   {},
	"int32":   {},
	"int64":   {},
	"uint":    {},
	"uint8":   {},
	"uint16":  {},
	"uint32":  {},
	"uint64":  {},
	"uintptr": {},
	"float32": {},
	"float64": {},
	"string":  {},
	"[]byte":  {},
}

const (
	protoTypeTimestamp = "Timestamp" // last segment, first will be *google_protobufX
	protoTypeJSON      = "JSONValue"
	protoTypeUUID      = "UUID"
	protoTypeUUIDValue = "UUIDValue"
	protoTypeResource  = "Identifier"
	protoTypeInet      = "InetValue"
	protoTimeOnly      = "TimeOnly"
)

// DB Engine Enum
const (
	ENGINE_UNSET = iota
	ENGINE_POSTGRES
)

type ORMBuilder struct {
	plugin         *protogen.Plugin
	ormableTypes   map[string]*OrmableType
	messages       map[string]struct{}
	fileImports    map[string]*fileImports // TODO: populate
	currentFile    string                  // TODO populate
	currentPackage string
	dbEngine       int
	stringEnums    bool
	gateway        bool
	suppressWarn   bool
}

func New(opts protogen.Options, request *pluginpb.CodeGeneratorRequest) (*ORMBuilder, error) {
	plugin, err := opts.New(request)
	if err != nil {
		return nil, err
	}

	builder := &ORMBuilder{
		plugin:       plugin,
		ormableTypes: make(map[string]*OrmableType),
		messages:     make(map[string]struct{}),
		fileImports:  make(map[string]*fileImports),
	}

	params := parseParameter(request.GetParameter())

	if strings.EqualFold(params["engine"], "postgres") {
		builder.dbEngine = ENGINE_POSTGRES
	} else {
		builder.dbEngine = ENGINE_UNSET
	}

	if strings.EqualFold(params["enums"], "string") {
		builder.stringEnums = true
	}

	if _, ok := params["gateway"]; ok {
		builder.gateway = true
	}

	if _, ok := params["quiet"]; ok {
		builder.suppressWarn = true
	}

	return builder, nil
}

func parseParameter(param string) map[string]string {
	paramMap := make(map[string]string)

	params := strings.Split(param, ",")
	for _, param := range params {
		if strings.Contains(param, "=") {
			kv := strings.Split(param, "=")
			paramMap[kv[0]] = kv[1]
			continue
		}
		paramMap[param] = ""
	}

	return paramMap
}

type OrmableType struct {
	File       *protogen.File
	Fields     map[string]*Field
	Methods    map[string]*autogenMethod
	Name       string
	OriginName string
	Package    string
}

func NewOrmableType(orignalName string, pkg string, file *protogen.File) *OrmableType {
	return &OrmableType{
		Name:    orignalName,
		Package: pkg,
		File:    file,
		Fields:  make(map[string]*Field),
		Methods: make(map[string]*autogenMethod),
	}
}

type Field struct {
	*gorm.GormFieldOptions
	ParentGoType   string
	Type           string
	Package        string
	ParentOrigName string
}

type autogenMethod struct {
}

type fileImports struct {
	wktPkgName      string
	packages        map[string]*pkgImport
	typesToRegister []string
	stdImports      []string
}

func newFileImports() *fileImports {
	return &fileImports{packages: make(map[string]*pkgImport)}
}

type pkgImport struct {
	packagePath string
	alias       string
}

func (b *ORMBuilder) Generate() (*pluginpb.CodeGeneratorResponse, error) {
	for _, protoFile := range b.plugin.Files {
		// TODO: set current file and newFileImport
		b.fileImports[*protoFile.Proto.Name] = newFileImports()

		// first traverse: preload the messages
		for _, message := range protoFile.Messages {
			if message.Desc.IsMapEntry() {
				continue
			}

			typeName := string(message.Desc.Name())
			b.messages[typeName] = struct{}{}

			if isOrmable(message) {
				ormable := NewOrmableType(typeName, string(protoFile.GoPackageName), protoFile)
				// TODO: for some reason pluginv1 thinks that we can
				// override values in this map
				b.ormableTypes[typeName] = ormable
			}
		}

		// second traverse: parse basic fields
		for _, message := range protoFile.Messages {
			if isOrmable(message) {
				b.parseBasicFields(message)
			}
		}

		// third traverse: build associations
		// TODO: implent functions
		for _, message := range protoFile.Messages {
			typeName := string(message.Desc.Name())
			if isOrmable(message) {
				b.parseAssociations(message)
				o := b.getOrmable(typeName)
				if b.hasPrimaryKey(o) {
					_, fd := b.findPrimaryKey(o)
					fd.ParentOrigName = o.OriginName
				}
			}
		}

		for _, ot := range b.ormableTypes {
			fmt.Fprintf(os.Stderr, "ormable type: %+v\n", ot.Name)
			for name, field := range ot.Fields {
				fmt.Fprintf(os.Stderr, "name: %s, field: %+v\n", name, field.Type)
			}
		}

		// dumb files
		filename := protoFile.GeneratedFilenamePrefix + ".gorm.go"
		gormFile := b.plugin.NewGeneratedFile(filename, ".")
		gormFile.P("package ", protoFile.GoPackageName)
		gormFile.P("// this file is generated")
	}

	return b.plugin.Response(), nil
}

func (b *ORMBuilder) parseAssociations(msg *protogen.Message) {
	typeName := string(msg.Desc.Name()) // TODO: camelSnakeCase
	ormable := b.getOrmable(typeName)

	for _, field := range msg.Fields {
		options := field.Desc.Options().(*descriptorpb.FieldOptions)
		fieldOpts := getFieldOptions(options)
		if fieldOpts.GetDrop() {
			continue
		}

		fieldName := string(field.Desc.Name())  // TODO: camelCase
		fieldType := field.Desc.Kind().String() // was GoType
		fieldType = strings.Trim(fieldType, "[]*")
		parts := strings.Split(fieldType, ".")
		fieldTypeShort := parts[len(parts)-1]

		if b.isOrmable(fieldType) {
			if fieldOpts == nil {
				fieldOpts = &gorm.GormFieldOptions{}
			}
			assocOrmable := b.getOrmable(fieldType)

			if field.Desc.Cardinality() == protoreflect.Repeated {
				if fieldOpts.GetManyToMany() != nil {
					b.parseManyToMany(msg, ormable, fieldName, fieldTypeShort, assocOrmable, fieldOpts)
				} else {
					b.parseHasMany(msg, ormable, fieldName, fieldTypeShort, assocOrmable, fieldOpts)
				}
				fieldType = fmt.Sprintf("[]*%sORM", fieldType)
			} else {
				if fieldOpts.GetBelongsTo() != nil {
					b.parseBelongsTo(msg, ormable, fieldName, fieldTypeShort, assocOrmable, fieldOpts)
				} else {
					b.parseHasOne(msg, ormable, fieldName, fieldTypeShort, assocOrmable, fieldOpts)
				}
				fieldType = fmt.Sprintf("*%sORM", fieldType)
			}

			// Register type used, in case it's an imported type from another package
			b.GetFileImports().typesToRegister = append(b.GetFileImports().typesToRegister, fieldType) // maybe we need other fields type
			ormable.Fields[fieldName] = &Field{Type: fieldType, GormFieldOptions: fieldOpts}
		}
	}
}

func (b *ORMBuilder) hasPrimaryKey(ormable *OrmableType) bool {
	// TODO: implement me
	return false
}

func (b *ORMBuilder) isOrmable(fieldType string) bool {
	// TODO: implement me
	return false
}

func (b *ORMBuilder) findPrimaryKey(ormable *OrmableType) (string, *Field) {
	// TODO: implement me
	return "", &Field{}
}

func (b *ORMBuilder) getOrmable(typeName string) *OrmableType {
	// TODO: implement me
	return &OrmableType{}
}

func (b *ORMBuilder) setFile(file string, pkg string) {
	b.currentFile = file
	b.currentPackage = pkg
	// b.Generator.SetFile(file) // TODO: do we need know current file?
}

func (p *ORMBuilder) parseManyToMany(msg *protogen.Message, ormable *OrmableType, fieldName string, fieldType string, assoc *OrmableType, opts *gorm.GormFieldOptions) {
	// TODO: implement me
}

func (p *ORMBuilder) parseHasOne(msg *protogen.Message, parent *OrmableType, fieldName string, fieldType string, child *OrmableType, opts *gorm.GormFieldOptions) {
	// TODO: implement me
}

func (p *ORMBuilder) parseHasMany(msg *protogen.Message, parent *OrmableType, fieldName string, fieldType string, child *OrmableType, opts *gorm.GormFieldOptions) {
	// TODO: implement me
}

func (p *ORMBuilder) parseBelongsTo(msg *protogen.Message, child *OrmableType, fieldName string, fieldType string, parent *OrmableType, opts *gorm.GormFieldOptions) {
	// TODO: implement me
}

func (b *ORMBuilder) parseBasicFields(msg *protogen.Message) {
	typeName := string(msg.Desc.Name())
	fmt.Fprintf(os.Stderr, "parseBasicFields -> : %s\n", typeName)

	ormable, ok := b.ormableTypes[typeName]
	if !ok {
		panic("typeName should be found")
	}
	ormable.Name = fmt.Sprintf("%sORM", typeName) // TODO: there are no reason to do it here

	for _, field := range msg.Fields {
		fd := field.Desc
		options := fd.Options().(*descriptorpb.FieldOptions)
		gormOptions := getFieldOptions(options)
		if gormOptions == nil {
			gormOptions = &gorm.GormFieldOptions{}
		}
		if gormOptions.GetDrop() {
			fmt.Fprintf(os.Stderr, "droping field: %s, %+v -> %t\n",
				field.Desc.TextName(), gormOptions, gormOptions.GetDrop())
			continue
		}

		tag := gormOptions.Tag
		fieldName := string(fd.Name())  // TODO: move to camelCase
		fieldType := fd.Kind().String() // TODO: figure out GoType analog

		fmt.Fprintf(os.Stderr, "field name: %s, type: %s, tag: %+v\n",
			fieldName, fieldType, tag)

		var typePackage string

		if b.dbEngine == ENGINE_POSTGRES && b.IsAbleToMakePQArray(fieldType) {
			switch fieldType {
			case "[]bool":
				fieldType = fmt.Sprintf("%s.BoolArray", b.Import(pqImport))
				gormOptions.Tag = tagWithType(tag, "bool[]")
			case "[]float64":
				fieldType = fmt.Sprintf("%s.Float64Array", b.Import(pqImport))
				gormOptions.Tag = tagWithType(tag, "float[]")
			case "[]int64":
				fieldType = fmt.Sprintf("%s.Int64Array", b.Import(pqImport))
				gormOptions.Tag = tagWithType(tag, "integer[]")
			case "[]string":
				fieldType = fmt.Sprintf("%s.StringArray", b.Import(pqImport))
				gormOptions.Tag = tagWithType(tag, "text[]")
			default:
				continue
			}
		} else if field.Enum != nil {
			fmt.Fprintf(os.Stderr, "field: %s is a enum\n", field.GoName)
			fieldType = "int32"
			if b.stringEnums {
				fieldType = "string"
			}
		} else if field.Message != nil {
			fmt.Fprintf(os.Stderr, "field: %s is a message\n", field.GoName)
		}

		fmt.Fprintf(os.Stderr, "detected field type is -> %s\n", fieldType)

		if tName := gormOptions.GetReferenceOf(); tName != "" {
			if _, ok := b.messages[tName]; !ok {
				panic("unknow")
			}
		}

		f := &Field{
			GormFieldOptions: gormOptions,
			ParentGoType:     "",
			Type:             fieldType,
			Package:          typePackage,
			ParentOrigName:   typeName,
		}

		ormable.Fields[fieldName] = f
	}

	gormMsgOptions := getMessageOptions(msg)
	if gormMsgOptions.GetMultiAccount() {
		if accID, ok := ormable.Fields["AccountID"]; !ok {
			ormable.Fields["AccountID"] = &Field{Type: "string"}
		} else if accID.Type != "string" {
			panic("cannot include AccountID field")
		}
	}

	// TODO: GetInclude
	for _, field := range gormMsgOptions.GetInclude() {
		fieldName := field.GetName() // TODO: camel case
		if _, ok := ormable.Fields[fieldName]; !ok {
			b.addIncludedField(ormable, field)
		} else {
			panic("cound not include")
		}
	}
}

func (b *ORMBuilder) addIncludedField(ormable *OrmableType, field *gorm.ExtraField) {
	fieldName := field.GetName() // TODO: CamelCase
	isPtr := strings.HasPrefix(field.GetType(), "*")
	rawType := strings.TrimPrefix(field.GetType(), "*")
	// cut off any package subpaths
	rawType = rawType[strings.LastIndex(rawType, ".")+1:]
	var typePackage string
	// Handle types with a package defined
	if field.GetPackage() != "" {
		alias := b.Import(field.GetPackage())
		rawType = fmt.Sprintf("%s.%s", alias, rawType)
		typePackage = field.GetPackage()
	} else {
		// Handle types without a package defined
		if _, ok := builtinTypes[rawType]; ok {
			// basic type, 100% okay, no imports or changes needed
		} else if rawType == "Time" {
			// b.UsingGoImports(stdTimeImport) // TODO: missing UsingGoImports
			typePackage = stdTimeImport
			rawType = fmt.Sprintf("%s.Time", typePackage)
		} else if rawType == "UUID" {
			rawType = fmt.Sprintf("%s.UUID", b.Import(uuidImport))
			typePackage = uuidImport
		} else if field.GetType() == "Jsonb" && b.dbEngine == ENGINE_POSTGRES {
			rawType = fmt.Sprintf("%s.Jsonb", b.Import(gormpqImport))
			typePackage = gormpqImport
		} else if rawType == "Inet" {
			rawType = fmt.Sprintf("%s.Inet", b.Import(gtypesImport))
			typePackage = gtypesImport
		} else {
			fmt.Fprintf(os.Stderr, "TODO: Warning")
			// p.warning(`included field %q of type %q is not a recognized special type, and no package specified. This type is assumed to be in the same package as the generated code`,
			// 	field.GetName(), field.GetType())
		}
	}
	if isPtr {
		rawType = fmt.Sprintf("*%s", rawType)
	}
	ormable.Fields[fieldName] = &Field{Type: rawType, Package: typePackage, GormFieldOptions: &gorm.GormFieldOptions{Tag: field.GetTag()}}
}

func getFieldOptions(options *descriptorpb.FieldOptions) *gorm.GormFieldOptions {
	if options == nil {
		return nil
	}

	v := proto.GetExtension(options, gorm.E_Field)
	if v == nil {
		return nil
	}

	opts, ok := v.(*gorm.GormFieldOptions)
	if !ok {
		return nil
	}

	return opts
}

// retrieves the GormMessageOptions from a message
func getMessageOptions(message *protogen.Message) *gorm.GormMessageOptions {
	options := message.Desc.Options()
	if options == nil {
		return nil
	}
	v := proto.GetExtension(options, gorm.E_Opts)
	if v != nil {
		return nil
	}

	opts, ok := v.(*gorm.GormMessageOptions)
	if !ok {
		return nil
	}

	return opts
}

func isOrmable(message *protogen.Message) bool {
	desc := message.Desc
	options := desc.Options()

	m, ok := proto.GetExtension(options, gorm.E_Opts).(*gorm.GormMessageOptions)
	if !ok || m == nil {
		return false
	}

	return m.Ormable
}

func (b *ORMBuilder) IsAbleToMakePQArray(fieldType string) bool {
	switch fieldType {
	case "[]bool":
		return true
	case "[]float64":
		return true
	case "[]int64":
		return true
	case "[]string":
		return true
	default:
		return false
	}
}

func (b *ORMBuilder) Import(packagePath string) string {
	subpath := packagePath[strings.LastIndex(packagePath, "/")+1:]
	// package will always be suffixed with an integer to prevent any collisions
	// with standard package imports
	for i := 1; ; i++ {
		newAlias := fmt.Sprintf("%s%d", strings.Replace(subpath, ".", "_", -1), i)
		if pkg, ok := b.GetFileImports().packages[newAlias]; ok {
			if packagePath == pkg.packagePath {
				return pkg.alias
			}
		} else {
			b.GetFileImports().packages[newAlias] = &pkgImport{packagePath: packagePath, alias: newAlias}
			return newAlias
		}
	}
}

func (b *ORMBuilder) GetFileImports() *fileImports {
	return b.fileImports[b.currentFile]
}

func tagWithType(tag *gorm.GormTag, typename string) *gorm.GormTag {
	if tag == nil {
		tag = &gorm.GormTag{}
	}

	tag.Type = typename
	return tag
}
