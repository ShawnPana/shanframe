//go:build !darwin || !cgo

package input

type Injector struct{}

func Supported() bool                           { return false }
func Authorized() bool                          { return false }
func RequestPermission()                        {}
func New() *Injector                            { return &Injector{} }
func DisplaySize() (w, h float64)               { return 0, 0 }
func (in *Injector) Move(nx, ny float64)        {}
func (in *Injector) Button(b int, down bool)    {}
func (in *Injector) Wheel(dx, dy float64)       {}
func (in *Injector) Key(name string, down bool) {}
func (in *Injector) Text(s string)              {}
func (in *Injector) ReleaseAll()                {}
