package deepequal

import "reflect"

func IgnoringEmpty(a, b any) bool {
	return values(reflect.ValueOf(a), reflect.ValueOf(b))
}

func values(a, b reflect.Value) bool {
	if a.Kind() != b.Kind() {
		return false
	}

	switch a.Kind() {
	case reflect.Slice:
		if a.Len() != b.Len() {
			return false
		}

		for i := range a.Len() {
			if !values(a.Index(i), b.Index(i)) {
				return false
			}
		}

		return true
	case reflect.Struct:
		for i := range a.NumField() {
			if !a.Type().Field(i).IsExported() {
				return reflect.DeepEqual(a.Interface(), b.Interface())
			}
		}

		for i := range a.NumField() {
			if !values(a.Field(i), b.Field(i)) {
				return false
			}
		}

		return true
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}

		return values(a.Elem(), b.Elem())
	case reflect.Map:
		if a.Len() != b.Len() {
			return false
		}

		for _, k := range a.MapKeys() {
			if v := b.MapIndex(k); !v.IsValid() || !values(a.MapIndex(k), v) {
				return false
			}
		}

		return true
	}

	return reflect.DeepEqual(a.Interface(), b.Interface())
}
