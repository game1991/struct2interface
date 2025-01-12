package struct2interface

import (
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"io/fs"
	"io/ioutil"
	"log"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/tools/imports"
)

type makeInterfaceFile struct {
	DirPath    string
	PkgName    string
	Structs    []string
	TypeDoc    map[string]string
	AllMethods map[string][]string
	AllImports []string
}

type Method struct {
	Code string
	Docs []string
}

func (m *Method) Lines() []string {
	var lines []string
	lines = append(lines, m.Docs...)
	lines = append(lines, m.Code)
	return lines
}

func getReceiverTypeName(src []byte, fl interface{}) (string, *ast.FuncDecl) {
	fd, ok := fl.(*ast.FuncDecl)
	if !ok {
		return "", nil
	}
	t, err := getReceiverType(fd)
	if err != nil {
		return "", nil
	}
	st := string(src[t.Pos()-1 : t.End()-1])
	if len(st) > 0 && st[0] == '*' {
		st = st[1:]
	}
	return st, fd
}

func getReceiverType(fd *ast.FuncDecl) (ast.Expr, error) {
	if fd.Recv == nil {
		return nil, fmt.Errorf("fd is not a method, it is a function")
	}
	return fd.Recv.List[0].Type, nil
}

func formatFieldList(src []byte, fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var parts []string
	for _, l := range fl.List {
		names := make([]string, len(l.Names))
		for i, n := range l.Names {
			names[i] = n.Name
		}
		t := string(src[l.Type.Pos()-1 : l.Type.End()-1])

		if len(names) > 0 {
			typeSharingArgs := strings.Join(names, ", ")
			parts = append(parts, fmt.Sprintf("%s %s", typeSharingArgs, t))
		} else {
			parts = append(parts, t)
		}
	}
	return parts
}

func parseStruct(src []byte) (pkgName string, structs []string, methods map[string][]Method, imports []string, typeDoc map[string]string, err error) {
	fset := token.NewFileSet()
	a, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return
	}

	pkgName = a.Name.Name

	for _, i := range a.Imports {
		if i.Name != nil {
			imports = append(imports, fmt.Sprintf("%s %s", i.Name.String(), i.Path.Value))
		} else {
			imports = append(imports, i.Path.Value)
		}
	}

	methods = make(map[string][]Method)
	for _, d := range a.Decls {
		if structName, fd := getReceiverTypeName(src, d); structName != "" {
			// 私有方法
			if !fd.Name.IsExported() {
				continue
			}
			params := formatFieldList(src, fd.Type.Params)
			ret := formatFieldList(src, fd.Type.Results)
			method := fmt.Sprintf("%s(%s) (%s)", fd.Name.String(), strings.Join(params, ", "), strings.Join(ret, ", "))
			var docs []string
			if fd.Doc != nil {
				for _, d := range fd.Doc.List {
					docs = append(docs, string(src[d.Pos()-1:d.End()-1]))
				}
			}
			if _, ok := methods[structName]; !ok {
				structs = append(structs, structName)
			}

			methods[structName] = append(methods[structName], Method{
				Code: method,
				Docs: docs,
			})
		}
	}

	typeDoc = make(map[string]string)
	for _, t := range doc.New(&ast.Package{Files: map[string]*ast.File{"": a}}, "", doc.AllDecls).Types {
		typeDoc[t.Name] = strings.TrimSuffix(t.Doc, "\n")
	}

	return
}

func formatCode(code string) ([]byte, error) {
	opts := &imports.Options{
		TabIndent: true,
		TabWidth:  2,
		Fragment:  true,
		Comments:  true,
	}

	formatcode, err := imports.Process("", []byte(code), opts)
	if err != nil {
		return nil, err
	}

	if string(formatcode) == code {
		return formatcode, err
	}

	return formatCode(string(formatcode))
}

func makeInterfaceHead(pkgName string, imports []string) []string {
	output := []string{
		"// Code generated by struct2interface; DO NOT EDIT.",
		"",
		"package " + pkgName,
		"import (",
	}
	output = append(output, imports...)
	output = append(output,
		")",
		"",
	)
	return output
}

func makeInterfaceBody(output []string, ifaceComment map[string]string, structName string, methods []string) []string {

	comment := strings.TrimSuffix(strings.Replace(ifaceComment[structName], "\n", "\n//\t", -1), "\n//\t")
	if len(strings.TrimSpace(comment)) > 0 {
		output = append(output, fmt.Sprintf("// %s", comment))
	}

	output = append(output, fmt.Sprintf("type %s interface {", structName+"Interface"))
	output = append(output, methods...)
	output = append(output, "}")
	return output
}

func createFile(objs map[string][]*makeInterfaceFile) error {
	for dir, obj := range objs {
		if len(obj) == 0 {
			continue
		}

		var (
			startTime         = time.Now()
			firstObj          = obj[0]
			pkgName           = firstObj.PkgName
			typeDoc           = firstObj.TypeDoc
			mapStructMethods  = make(map[string][]string)
			listStructMethods = make([]string, 0)
			structAllImports  = make([]string, 0)
		)

		for _, file := range obj {
			for _, structName := range file.Structs {
				if _, ok := mapStructMethods[structName]; ok {
					mapStructMethods[structName] = append(mapStructMethods[structName], file.AllMethods[structName]...)
				} else {
					mapStructMethods[structName] = file.AllMethods[structName]
					listStructMethods = append(listStructMethods, structName)
				}

				structAllImports = append(structAllImports, file.AllImports...)
			}
		}

		output := makeInterfaceHead(pkgName, structAllImports)

		for _, structName := range listStructMethods {
			methods, ok := mapStructMethods[structName]
			if !ok {
				continue
			}
			output = makeInterfaceBody(output, typeDoc, structName, methods)
		}

		code := strings.Join(output, "\n")
		result, err := formatCode(code)
		if err != nil {
			fmt.Printf("[struct2interface] %s \n", "formatCode error")
			return err
		}
		var fileName = filepath.Join(dir, "interface_"+pkgName+".go")
		if err = ioutil.WriteFile(fileName, result, 0644); err != nil {
			return err
		}
		fmt.Printf("[struct2interface] %s %s %s \n", "parsing", time.Since(startTime).String(), fileName)
	}

	return nil
}

func makeFile(file string) (*makeInterfaceFile, error) {
	var (
		allMethods = make(map[string][]string)
		allImports = make([]string, 0)
		iset       = make(map[string]struct{})
		typeDoc    = make(map[string]string)
	)

	src, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}

	pkgName, structSlice, methods, importList, parsedTypeDoc, err := parseStruct(src)
	if err != nil {
		fmt.Printf("[struct2interface] %s, err: %s\n", "file parseStruct error", err.Error())
		return nil, err
	}

	if len(methods) == 0 {
		return nil, nil
	}

	for _, i := range importList {
		if _, ok := iset[i]; !ok {
			allImports = append(allImports, i)
			iset[i] = struct{}{}
		}
	}

	for structName, mm := range methods {
		typeDoc[structName] = fmt.Sprintf("%s ...\n%s", structName+"Interface", parsedTypeDoc[structName])
		for _, m := range mm {
			allMethods[structName] = append(allMethods[structName], m.Lines()...)
		}
	}

	return &makeInterfaceFile{
		DirPath:    filepath.Dir(file),
		PkgName:    pkgName,
		Structs:    structSlice,
		TypeDoc:    typeDoc,
		AllMethods: allMethods,
		AllImports: allImports,
	}, nil
}

func MakeDir(dir string) error {
	var mapDirPath = make(map[string][]*makeInterfaceFile)
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(filepath.Base(path), "interface_") {
			return nil
		}
		if strings.HasPrefix(filepath.Base(path), "mock_") {
			return nil
		}
		if !strings.HasSuffix(filepath.Base(path), ".go") {
			return nil
		}

		result, err := makeFile(path)
		if err != nil {
			log.Panic("struct2interface.Make failed,", err.Error(), path)
		} else if result == nil {
			return nil
		}

		if _, ok := mapDirPath[filepath.Dir(path)]; ok {
			mapDirPath[filepath.Dir(path)] = append(mapDirPath[filepath.Dir(path)], result)
		} else {
			mapDirPath[filepath.Dir(path)] = []*makeInterfaceFile{result}
		}

		return nil
	}); err != nil {
		fmt.Printf("[struct2interface] %s \n", err.Error())
		return err
	}

	return createFile(mapDirPath)
}
