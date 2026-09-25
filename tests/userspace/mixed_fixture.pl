#!/usr/bin/perl
# Temporary loopback-only generated traffic and fixed read-only diagnostics.
# No backend password/key is read. Run with a bounded transient service.
use strict;use warnings;
use IO::Socket::INET;use JSON::PP;use Digest::SHA;use Time::HiRes qw(time sleep clock_gettime CLOCK_MONOTONIC);
use Fcntl qw(:flock);use POSIX qw(WNOHANG);
my ($directory,$port,$run)=@ARGV;
die "usage: fixture.pl private-directory port run-id\n" unless defined($run)&&$run=~/^[A-Za-z0-9_-]{1,80}$/&&$port=~/^\d+$/&&$port>1024&&$port<65536&&-d $directory;
umask 0077;
my $pattern=join('',map {chr(($_*31+($_>>8)+17)%256)} 0..32767);
my $listener=IO::Socket::INET->new(LocalAddr=>'127.0.0.1',LocalPort=>$port,Listen=>64,ReuseAddr=>1,Proto=>'tcp') or die $!;
my %children;my $running=1;my $sequence=0;
$SIG{TERM}=sub{$running=0;close $listener};$SIG{INT}=$SIG{TERM};$SIG{PIPE}='IGNORE';
sub write_all {my ($socket,$data)=@_;my $offset=0;while($offset<length($data)){my $n=syswrite($socket,$data,length($data)-$offset,$offset);die "socket write failed" unless defined($n)&&$n>0;$offset+=$n;}return $offset;}
sub slurp {my ($path)=@_;open my $f,'<',$path or die "fixed diagnostic unavailable";local $/=undef;my $s=<$f>;close $f;die "oversized diagnostic" if length($s)>131072;return $s;}
sub telemetry {
 my $raw=slurp('/run/mptcp-userspace-landing/status.json');my $status=decode_json($raw);
 my $unit=`systemctl show mptcp-userspace-landing -p MainPID -p NRestarts -p ActiveState -p SubState -p Result`;
 return encode_json({status=>$status,unit=>$unit});
}
sub serve {
 my ($socket,$id)=@_;$SIG{ALRM}=sub{die "fixture request deadline"};alarm 205;
 my $started=time;my $monotonic=clock_gettime(CLOCK_MONOTONIC);my $path='';my $count=0;my $digest=Digest::SHA->new(256);my $error='';
 my $ok=eval {
  my $headers='';while(index($headers,"\r\n\r\n")<0){my $n=sysread($socket,my $part,2048);die "incomplete request" unless $n;$headers.=$part;die "headers too large" if length($headers)>8192;}
  ($path)=$headers=~/\AGET ([^ ]+) HTTP\/1\.[01]\r\n/;die "GET only" unless defined($path);
  my $body;my $size=0;my $duration=0;my $hold=0;
  if($path eq '/health') {$body=encode_json({run_id=>$run,loopback_only=>JSON::PP::true});}
  elsif($path eq '/telemetry') {$body=telemetry();}
  elsif($path=~m{^/short\?i=[A-Za-z0-9_-]+$}) {$size=1537;}
  elsif($path=~m{^/bytes\?n=(\d+)&i=[A-Za-z0-9_-]+$}) {$size=$1;die "byte limit" if $size>16777216||$size<1;}
  elsif($path=~m{^/bulk\?seconds=(\d+)&i=[A-Za-z0-9_-]+$}) {$duration=$1;die "bulk segment limit" if $duration<1||$duration>15;}
  elsif($path=~m{^/hold\?seconds=(\d+)&i=[A-Za-z0-9_-]+$}) {$duration=$1;$hold=1;die "hold limit" if $duration<1||$duration>185;}
  else {die "unsupported fixed fixture path";}
  my $header="HTTP/1.1 200 OK\r\nConnection: close\r\nCache-Control: no-store\r\nX-MPX-Test-ID: $run\r\nContent-Type: application/octet-stream\r\n";
  $header.='Content-Length: '.(defined($body)?length($body):$size)."\r\n" if defined($body)||!$duration;
  write_all($socket,$header."\r\n");
  if(defined($body)){write_all($socket,$body);$count=length($body);$digest->add($body);}
  else {
   my $begin=clock_gettime(CLOCK_MONOTONIC);
   while(($duration&&clock_gettime(CLOCK_MONOTONIC)-$begin<$duration)||(!$duration&&$count<$size)){
    my $length=$hold?64:32768;$length=$size-$count if !$duration&&$length>$size-$count;
    my $offset=$count%length($pattern);my $chunk=substr($pattern.$pattern,$offset,$length);
    write_all($socket,$chunk);$digest->add($chunk);$count+=$length;
    if($hold){my $left=$duration-(clock_gettime(CLOCK_MONOTONIC)-$begin);sleep($left<5?$left:5) if $left>0;}
   }
  }
  1;
 };
 $error=$@||'' unless $ok;$error=~s/[\r\n]+/ /g;
 my $entry={request_id=>$id,run_id=>$run,path=>$path//'',bytes=>$count,sha256=>$digest->hexdigest,seconds=>clock_gettime(CLOCK_MONOTONIC)-$monotonic,started_remote_epoch=>$started,success=>$ok?JSON::PP::true:JSON::PP::false,error=>$error};
 open my $log,'>>',"$directory/requests.jsonl" or die "fixture log unavailable";flock($log,LOCK_EX);print $log encode_json($entry),"\n";close $log;
 close $socket;alarm 0;
}
while($running){
 for my $pid(keys %children){delete $children{$pid} if waitpid($pid,WNOHANG)>0;}
 my $socket=$listener->accept();next unless $socket;
 if(keys(%children)>=64){close $socket;next;}
 my $id=++$sequence;my $pid=fork();if(!defined($pid)){close $socket;next;}
 if($pid==0){$SIG{TERM}=sub{die "fixture terminated"};close $listener;%children=();eval {serve($socket,$id)};exit 0;}
 $children{$pid}=1;close $socket;
}
close $listener;kill 'TERM',keys %children if %children;for my $pid(keys %children){waitpid($pid,0);}
